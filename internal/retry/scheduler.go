// Package retry provides the background scheduler that retries failed AI
// imports according to the configured retry count and backoff.
package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/state"
)

// AIRetrier is the interface the Scheduler uses to re-run AI processing and
// write error text to notes. It is implemented by *importer.Importer.
type AIRetrier interface {
	// RetryAI re-runs AI processing for an already-imported record.
	// On success it updates the note and saves the record; on failure it
	// returns the error and leaves note/record unchanged.
	RetryAI(ctx context.Context, record state.Record) error

	// WriteNoteError writes an error message into the note's Summary and OCR
	// marker blocks. Called when retries are exhausted or the error is
	// non-retryable.
	WriteNoteError(record state.Record, msg string) error
}

// Scheduler polls the state DB for failed AI imports and retries them at the
// configured backoff interval.
type Scheduler struct {
	store *state.Store
	imp   AIRetrier
	cfg   config.RetryConfig
	locks *importer.LockManager
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once // guards done channel close
}

// NewScheduler constructs a Scheduler. Call Start to begin polling.
func NewScheduler(store *state.Store, imp AIRetrier, cfg config.RetryConfig, lockManagers ...*importer.LockManager) *Scheduler {
	locks := importer.NewLockManager()
	if len(lockManagers) > 0 && lockManagers[0] != nil {
		locks = lockManagers[0]
	}
	return &Scheduler{
		store: store,
		imp:   imp,
		cfg:   cfg,
		locks: locks,
		done:  make(chan struct{}),
	}
}

// Start launches the background polling goroutine. It is safe to call once.
// The goroutine exits when ctx is cancelled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.cfg.BackoffDuration)
		defer ticker.Stop()
		s.runOnce(ctx)
		for {
			select {
			case <-ticker.C:
				s.runOnce(ctx)
			case <-ctx.Done():
				return
			case <-s.done:
				return
			}
		}
	}()
}

// Stop signals the scheduler goroutine to exit and waits for it to finish.
// Safe to call concurrently; only the first call has any effect.
func (s *Scheduler) Stop() {
	s.once.Do(func() { close(s.done) })
	s.wg.Wait()
}

// runOnce performs a single scan-and-retry pass. It is safe to call directly
// in tests without starting the goroutine.
func (s *Scheduler) runOnce(ctx context.Context) {
	records, err := s.store.GetPendingAndFailedAIImports()
	if err != nil {
		slog.Error("retry scheduler: state query failed", "err", err)
		return
	}
	for _, rec := range records {
		if rec.AIStatus == state.AIStatusFailed && (!s.cfg.Enabled || rec.AIRetryCount >= s.cfg.MaxRetries) {
			continue // already exhausted
		}
		if rec.AIStatus == state.AIStatusFailed && time.Since(rec.AILastRetryAt) < s.cfg.BackoffDuration {
			continue // too soon since last attempt
		}
		s.processRecord(ctx, rec)
	}
}

// processRecord attempts one AI retry for rec and updates the state/note
// according to the outcome.
func (s *Scheduler) processRecord(ctx context.Context, rec state.Record) {
	unlock := s.locks.Lock(rec.VaultNotePath)
	defer unlock()

	// The scan snapshot can be stale if an upload completed while this worker
	// was waiting. Re-read under the shared note lock and only process the same
	// current import identity.
	current, err := s.store.GetBySourcePath(rec.SourcePath)
	if err != nil {
		slog.Error("retry scheduler: state read failed", "source", rec.SourcePath, "err", err)
		return
	}
	if current == nil || current.SHA256 != rec.SHA256 || current.VaultNotePath != rec.VaultNotePath ||
		(current.AIStatus != state.AIStatusPending && current.AIStatus != state.AIStatusFailed) {
		return
	}
	rec = *current
	if rec.AIStatus == state.AIStatusFailed && (!s.cfg.Enabled || rec.AIRetryCount >= s.cfg.MaxRetries || time.Since(rec.AILastRetryAt) < s.cfg.BackoffDuration) {
		return
	}
	attempt := rec.AIRetryCount + 1
	slog.Info("retry scheduler: attempting retry",
		"source", rec.SourcePath, "attempt", attempt, "max", s.cfg.MaxRetries)

	err = s.imp.RetryAI(ctx, rec)
	if err == nil {
		// RetryAI already updated the record and note on success.
		slog.Info("retry scheduler: retry succeeded",
			"source", rec.SourcePath, "attempt", attempt)
		return
	}

	// Non-retryable: either HTTP auth/client error, or missing vault PDF.
	// Both are permanent failures that benefit from no further retries.
	retryable := !errors.Is(err, os.ErrNotExist) && ai.IsRetryable(err)

	if !retryable || !s.cfg.Enabled {
		// Jump straight to MaxRetries so this record is never polled again.
		rec.AIRetryCount = s.cfg.MaxRetries
	} else {
		rec.AIRetryCount++
	}
	rec.AIStatus = state.AIStatusFailed
	rec.AILastError = err.Error()
	rec.AILastRetryAt = time.Now().UTC()

	exhausted := !retryable || rec.AIRetryCount >= s.cfg.MaxRetries

	if exhausted {
		slog.Error("retry scheduler: retry exhausted",
			"source", rec.SourcePath, "attempt", attempt, "err", err)
	} else {
		slog.Info("retry scheduler: retry failed, will retry later",
			"source", rec.SourcePath, "attempt", attempt, "err", err)
	}
	noun := "attempts"
	if attempt == 1 {
		noun = "attempt"
	}
	msg := fmt.Sprintf("_AI failed after %d %s: %s_", attempt, noun, err.Error())
	if noteErr := s.imp.WriteNoteError(rec, msg); noteErr != nil {
		slog.Error("retry scheduler: failed to write error to note", "source", rec.SourcePath, "err", noteErr)
	}

	if saveErr := s.store.Put(&rec); saveErr != nil {
		slog.Error("retry scheduler: failed to save record",
			"source", rec.SourcePath, "err", saveErr)
	}
}
