package retry

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/state"
)

// openTestStore opens a throw-away BoltDB store in t.TempDir.
func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mockRetrier implements AIRetrier. On a nil retryResult it simulates a
// successful RetryAI by updating the record in the store.
type mockRetrier struct {
	store        *state.Store
	retryResult  error
	writeNoteErr error
	retryCalls   int
	writtenMsgs  []string
}

func (m *mockRetrier) RetryAI(ctx context.Context, record state.Record) error {
	m.retryCalls++
	if m.retryResult == nil && m.store != nil {
		record.AIStatus = state.AIStatusSuccess
		record.AIRetryCount++
		record.AILastError = ""
		record.AILastRetryAt = time.Now().UTC()
		_ = m.store.Put(&record)
	}
	return m.retryResult
}

func (m *mockRetrier) WriteNoteError(record state.Record, msg string) error {
	m.writtenMsgs = append(m.writtenMsgs, msg)
	return m.writeNoteErr
}

// defaultCfg returns a RetryConfig suitable for tests: very short backoff so
// the elapsed-time guard is easy to satisfy.
func defaultCfg() config.RetryConfig {
	return config.RetryConfig{
		Enabled:         true,
		MaxRetries:      3,
		Backoff:         "1ms",
		BackoffDuration: time.Millisecond,
	}
}

// failedRec builds a state.Record with AIStatus=failed and an old enough
// AILastRetryAt that any BackoffDuration in tests is satisfied.
func failedRec(sourcePath string) state.Record {
	return state.Record{
		SourcePath:    sourcePath,
		SHA256:        "deadbeef",
		VaultPDFPath:  "pdfs/test.pdf",
		VaultNotePath: "notes/test.md",
		ImportedAt:    time.Now().UTC(),
		AIStatus:      state.AIStatusFailed,
		AIRetryCount:  0,
		AILastError:   "gemini 503: service unavailable",
		AILastRetryAt: time.Now().UTC().Add(-time.Hour), // definitely elapsed
	}
}

func pendingRec(sourcePath string) state.Record {
	rec := failedRec(sourcePath)
	rec.AIStatus = state.AIStatusPending
	rec.AILastRetryAt = time.Time{}
	return rec
}

func TestRunOnceProcessesPendingRecordWhenRetriesDisabled(t *testing.T) {
	store := openTestStore(t)
	rec := pendingRec("Syncs/pending.pdf")
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}
	cfg := defaultCfg()
	cfg.Enabled = false
	mock := &mockRetrier{store: store}
	s := NewScheduler(store, mock, cfg)
	s.runOnce(context.Background())
	if mock.retryCalls != 1 {
		t.Fatalf("RetryAI calls = %d, want 1", mock.retryCalls)
	}
	got, err := store.GetBySourcePath(rec.SourcePath)
	if err != nil || got == nil || got.AIStatus != state.AIStatusSuccess {
		t.Fatalf("pending record not completed: record=%+v err=%v", got, err)
	}
}

func TestProcessRecordDoesNotOverwriteNewerImport(t *testing.T) {
	store := openTestStore(t)
	stale := failedRec("Syncs/race.pdf")
	if err := store.Put(&stale); err != nil {
		t.Fatal(err)
	}
	newer := stale
	newer.SHA256 = "newer"
	newer.AIStatus = state.AIStatusPending
	if err := store.Put(&newer); err != nil {
		t.Fatal(err)
	}
	mock := &mockRetrier{store: store}
	s := NewScheduler(store, mock, defaultCfg())
	s.processRecord(context.Background(), stale)
	if mock.retryCalls != 0 {
		t.Fatalf("RetryAI calls = %d, want 0 for stale snapshot", mock.retryCalls)
	}
	got, err := store.GetBySourcePath(newer.SourcePath)
	if err != nil || got == nil || got.SHA256 != "newer" || got.AIStatus != state.AIStatusPending {
		t.Fatalf("newer record overwritten: record=%+v err=%v", got, err)
	}
}

// ----- Test 5.1: eligible record is processed -----

func TestRunOnceProcessesEligibleRecord(t *testing.T) {
	store := openTestStore(t)
	rec := failedRec("Syncs/eligible.pdf")
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	mock := &mockRetrier{store: store} // retryResult nil → success
	s := NewScheduler(store, mock, defaultCfg())
	s.runOnce(context.Background())

	if mock.retryCalls != 1 {
		t.Errorf("RetryAI called %d times, want 1", mock.retryCalls)
	}
	// Record should now show success.
	got, err := store.GetBySourcePath("Syncs/eligible.pdf")
	if err != nil || got == nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if got.AIStatus != state.AIStatusSuccess {
		t.Errorf("AIStatus = %q, want %q", got.AIStatus, state.AIStatusSuccess)
	}
	if got.AIRetryCount != 1 {
		t.Errorf("AIRetryCount = %d, want 1", got.AIRetryCount)
	}
}

// ----- Test 5.2: exhausted record is NOT processed -----

func TestRunOnceSkipsExhaustedRecord(t *testing.T) {
	store := openTestStore(t)
	rec := failedRec("Syncs/exhausted.pdf")
	rec.AIRetryCount = 3 // == MaxRetries (3)
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	mock := &mockRetrier{}
	s := NewScheduler(store, mock, defaultCfg())
	s.runOnce(context.Background())

	if mock.retryCalls != 0 {
		t.Errorf("RetryAI called %d times, want 0 for exhausted record", mock.retryCalls)
	}
}

// ----- Test 5.3: too-recent record is NOT processed -----

func TestRunOnceSkipsTooRecentRecord(t *testing.T) {
	store := openTestStore(t)
	rec := failedRec("Syncs/recent.pdf")
	rec.AILastRetryAt = time.Now().UTC() // just now
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	// Use a generous backoff so "just now" is definitely too recent.
	cfg := config.RetryConfig{
		Enabled:         true,
		MaxRetries:      3,
		Backoff:         "1h",
		BackoffDuration: time.Hour,
	}
	mock := &mockRetrier{}
	s := NewScheduler(store, mock, cfg)
	s.runOnce(context.Background())

	if mock.retryCalls != 0 {
		t.Errorf("RetryAI called %d times, want 0 for too-recent record", mock.retryCalls)
	}
}

// ----- Test 5.4: non-retryable error exhausts immediately -----

func TestRunOnceNonRetryableExhaustsImmediately(t *testing.T) {
	store := openTestStore(t)
	const maxRetries = 3
	rec := failedRec("Syncs/nonretryable.pdf")
	rec.AIRetryCount = 0
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	// 401 is non-retryable.
	nonRetryableErr := fmt.Errorf("retry import: %w", &ai.APIError{StatusCode: 401, Message: "API key invalid"})
	mock := &mockRetrier{retryResult: nonRetryableErr}
	cfg := config.RetryConfig{
		Enabled:         true,
		MaxRetries:      maxRetries,
		Backoff:         "1ms",
		BackoffDuration: time.Millisecond,
	}
	s := NewScheduler(store, mock, cfg)
	s.runOnce(context.Background())

	// RetryAI should have been called once.
	if mock.retryCalls != 1 {
		t.Errorf("RetryAI called %d times, want 1", mock.retryCalls)
	}
	// WriteNoteError should have been called with the error message.
	if len(mock.writtenMsgs) == 0 {
		t.Fatal("WriteNoteError was not called, want 1 call")
	}
	msg := mock.writtenMsgs[0]
	if !strings.Contains(msg, "API key invalid") {
		t.Errorf("note error message = %q, want it to contain 'API key invalid'", msg)
	}
	// Message must include the attempt count so the user knows it was tried.
	// AIRetryCount was 0, so attempt=1 → "after 1 attempt".
	if !strings.Contains(msg, "after 1 attempt") {
		t.Errorf("note error message = %q, want it to contain 'after 1 attempt'", msg)
	}
	// Record should be saved with AIRetryCount == MaxRetries so it won't be polled again.
	got, err := store.GetBySourcePath("Syncs/nonretryable.pdf")
	if err != nil || got == nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if got.AIRetryCount != maxRetries {
		t.Errorf("AIRetryCount = %d, want %d (MaxRetries)", got.AIRetryCount, maxRetries)
	}
}

// TestExhaustedRetryMessageIncludesAttemptCount verifies that when the last
// retryable retry is consumed the note error message contains the plural
// attempt count, e.g. "_AI failed after 3 attempts: ..._".
func TestExhaustedRetryMessageIncludesAttemptCount(t *testing.T) {
	store := openTestStore(t)
	const maxRetries = 3
	rec := failedRec("Syncs/exhausting.pdf")
	// Simulate two prior scheduler attempts: AIRetryCount=2, so attempt=3 is
	// the last allowed attempt (3 == MaxRetries after increment → exhausted).
	rec.AIRetryCount = 2
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	retryableErr := &ai.APIError{StatusCode: 503, Message: "service unavailable"}
	mock := &mockRetrier{retryResult: retryableErr}
	cfg := config.RetryConfig{
		Enabled:         true,
		MaxRetries:      maxRetries,
		Backoff:         "1ms",
		BackoffDuration: time.Millisecond,
	}
	s := NewScheduler(store, mock, cfg)
	s.runOnce(context.Background())

	if mock.retryCalls != 1 {
		t.Fatalf("RetryAI called %d times, want 1", mock.retryCalls)
	}
	if len(mock.writtenMsgs) == 0 {
		t.Fatal("WriteNoteError was not called; record should be exhausted after attempt 3")
	}
	msg := mock.writtenMsgs[0]
	// Must contain plural "attempts" (attempt=3) and the error text.
	if !strings.Contains(msg, "after 3 attempts") {
		t.Errorf("note error message = %q, want it to contain 'after 3 attempts'", msg)
	}
	if !strings.Contains(msg, "service unavailable") {
		t.Errorf("note error message = %q, want it to contain 'service unavailable'", msg)
	}
	// Confirm the message is well-formed italic markdown.
	if !strings.HasPrefix(msg, "_AI failed after") || !strings.HasSuffix(msg, "_") {
		t.Errorf("note error message = %q, want italic markdown wrapping", msg)
	}
}

// ----- Test 5.5: successful retry updates record and note -----

func TestRunOnceSuccessfulRetryWritesSuccessToRecord(t *testing.T) {
	store := openTestStore(t)
	rec := failedRec("Syncs/willsucceed.pdf")
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	mock := &mockRetrier{store: store} // nil retryResult → simulates success
	s := NewScheduler(store, mock, defaultCfg())
	s.runOnce(context.Background())

	if mock.retryCalls != 1 {
		t.Errorf("RetryAI called %d times, want 1", mock.retryCalls)
	}
	// No error should be written to the note.
	if len(mock.writtenMsgs) != 0 {
		t.Errorf("WriteNoteError called unexpectedly: %v", mock.writtenMsgs)
	}
	// Record should reflect success.
	got, err := store.GetBySourcePath("Syncs/willsucceed.pdf")
	if err != nil || got == nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if got.AIStatus != state.AIStatusSuccess {
		t.Errorf("AIStatus = %q, want %q", got.AIStatus, state.AIStatusSuccess)
	}
	if got.AIRetryCount != 1 {
		t.Errorf("AIRetryCount = %d, want 1", got.AIRetryCount)
	}
	if got.AILastError != "" {
		t.Errorf("AILastError = %q, want empty", got.AILastError)
	}
}

// ----- Test: retryable failure below max increments count and writes outcome -----

func TestRunOnceRetryableFailureBelowMaxIncrementsOnly(t *testing.T) {
	store := openTestStore(t)
	rec := failedRec("Syncs/transient.pdf")
	rec.AIRetryCount = 0
	if err := store.Put(&rec); err != nil {
		t.Fatal(err)
	}

	retryableErr := &ai.APIError{StatusCode: 503, Message: "service unavailable"}
	mock := &mockRetrier{retryResult: retryableErr}
	cfg := config.RetryConfig{
		Enabled:         true,
		MaxRetries:      3, // newCount=1 < 3, so not yet exhausted
		Backoff:         "1ms",
		BackoffDuration: time.Millisecond,
	}
	s := NewScheduler(store, mock, cfg)
	s.runOnce(context.Background())

	if len(mock.writtenMsgs) != 1 {
		t.Errorf("WriteNoteError calls = %d, want 1", len(mock.writtenMsgs))
	}
	// Record incremented.
	got, err := store.GetBySourcePath("Syncs/transient.pdf")
	if err != nil || got == nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if got.AIRetryCount != 1 {
		t.Errorf("AIRetryCount = %d, want 1", got.AIRetryCount)
	}
	if got.AIStatus != state.AIStatusFailed {
		t.Errorf("AIStatus = %q, want %q", got.AIStatus, state.AIStatusFailed)
	}
}
