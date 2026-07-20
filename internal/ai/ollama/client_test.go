package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"inkflow/internal/ai"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(ClientConfig{
		BaseURL:       srv.URL,
		Model:         "llama3.2-vision",
		Timeout:       2 * time.Second,
		OCRPrompt:     "Transcribe faithfully",
		SummaryPrompt: "Summarize as 3 bullets",
	}), srv
}

func TestProcessHappyPathSendsVisionRequestAndParsesResponse(t *testing.T) {
	var gotPath, gotMethod, gotContentType string
	var gotBody []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		inner, _ := json.Marshal(map[string]any{
			"ocr_text": "full transcription",
			"summary":  []string{"a", "b", "c"},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": string(inner)},
		})
	})

	pdf := []byte("fake pdf bytes")
	res, err := c.Process(context.Background(), bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OCR != "full transcription" || len(res.Summary) != 3 || res.Summary[0] != "a" {
		t.Errorf("result = %#v", res)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/chat" {
		t.Errorf("request = %s %s, want POST /api/chat", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	var request struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Content string   `json:"content"`
			Images  []string `json:"images"`
		} `json:"messages"`
		Format json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(gotBody, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "llama3.2-vision" || request.Stream || len(request.Messages) != 1 {
		t.Errorf("request = %#v", request)
	}
	if len(request.Messages[0].Images) != 1 || request.Messages[0].Images[0] != base64.StdEncoding.EncodeToString(pdf) {
		t.Errorf("images = %v, want base64 PDF", request.Messages[0].Images)
	}
	if !strings.Contains(request.Messages[0].Content, "Summarize as 3 bullets") || len(request.Format) == 0 {
		t.Errorf("structured prompt or format missing: %#v", request)
	}
}

func TestProcessHTTPErrorUsesSharedAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"model is loading"}`))
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *ai.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected shared ai.APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Message != "model is loading" {
		t.Errorf("APIError = %#v", apiErr)
	}
	if !ai.IsRetryable(err) {
		t.Error("service-unavailable error should be retryable")
	}
}

func TestProcessMalformedResponseFails(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": `{"ocr_text":"only OCR"}`},
		})
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "missing ocr_text or summary") {
		t.Fatalf("expected malformed payload error, got %v", err)
	}
}

func TestProcessUnescapesDoublyEscapedNewlines(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		inner, _ := json.Marshal(map[string]any{
			"ocr_text": `line one\nline two`,
			"summary":  []string{`bullet\nwith newline`},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": string(inner)}})
	})
	result, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.OCR, `\n`) || strings.Contains(result.Summary[0], `\n`) {
		t.Errorf("literal escaped newline was not repaired: %#v", result)
	}
}
