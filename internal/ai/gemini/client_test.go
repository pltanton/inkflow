package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(ClientConfig{
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Model:         "gemini-3.5-flash",
		Timeout:       2 * time.Second,
		OCRPrompt:     "Transcribe faithfully",
		SummaryPrompt: "Summarize as 3 bullets",
	})
	return c, srv
}

func TestProcessHappyPathParsesJSONResponse(t *testing.T) {
	var gotPath, gotQuery, gotAPIKeyHeader string
	var gotBody []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKeyHeader = r.Header.Get("X-Goog-Api-Key")
		gotBody, _ = io.ReadAll(r.Body)
		inner, _ := json.Marshal(map[string]any{
			"ocr_text": "full transcription",
			"summary":  []string{"a", "b", "c"},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{"text": string(inner)}},
				},
				"finishReason": "STOP",
			}},
		})
	})

	res, err := c.Process(context.Background(), bytes.NewReader([]byte("fake pdf bytes")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OCR != "full transcription" {
		t.Errorf("OCR = %q", res.OCR)
	}
	if len(res.Summary) != 3 || res.Summary[0] != "a" {
		t.Errorf("Summary = %v", res.Summary)
	}
	if gotPath != "/v1beta/models/gemini-3.5-flash:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAPIKeyHeader != "test-key" {
		t.Errorf("X-Goog-Api-Key = %q", gotAPIKeyHeader)
	}
	if strings.Contains(gotQuery, "key=") {
		t.Errorf("API key leaked into query string: %q", gotQuery)
	}
	if !bytes.Contains(gotBody, []byte(`"response_schema"`)) {
		t.Errorf("response_schema missing from request body: %s", gotBody)
	}
	if !bytes.Contains(gotBody, []byte(`"application/pdf"`)) {
		t.Errorf("PDF mime type not in request body: %s", gotBody)
	}
	if !bytes.Contains(gotBody, []byte(`"response_mime_type":"application/json"`)) {
		t.Errorf("response_mime_type missing: %s", gotBody)
	}
	if !bytes.Contains(gotBody, []byte("Summarize as 3 bullets")) {
		t.Errorf("summary prompt missing: %s", gotBody)
	}
}

func TestProcessAuthFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"API key invalid","status":"UNAUTHENTICATED"}}`))
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected error")
	}
	// Should extract just error.message, not the raw JSON body.
	if err.Error() != "gemini 401: API key invalid" {
		t.Fatalf("expected clean error message, got: %v", err)
	}
}

func TestProcessFailureFallsBackToRawBodyWhenNoMessageField(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream proxy dead"))
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream proxy dead") {
		t.Fatalf("expected raw body fallback, got: %v", err)
	}
}

func TestProcessServerFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`oops`))
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

func TestProcessSchemaViolation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// inner text isn't JSON at all
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{"text": "not json"}},
				},
				"finishReason": "STOP",
			}},
		})
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestProcessNetworkError(t *testing.T) {
	c := New(ClientConfig{
		Endpoint:      "http://127.0.0.1:1", // refused
		APIKey:        "test-key",
		Model:         "gemini-3.5-flash",
		Timeout:       200 * time.Millisecond,
		OCRPrompt:     "x",
		SummaryPrompt: "y",
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Errorf("API key leaked into network error: %v", err)
	}
}

func TestProcessTruncatedResponseSurfacesFinishReason(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		inner, _ := json.Marshal(map[string]any{
			"ocr_text": "partial transcription",
			"summary":  []string{"only one bullet"},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{"text": string(inner)}},
				},
				"finishReason": "MAX_TOKENS",
			}},
		})
	})
	_, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if !strings.Contains(err.Error(), "MAX_TOKENS") {
		t.Errorf("error does not mention finish reason: %v", err)
	}
}

func TestProcessUnescapesDoublyEscapedNewlines(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		inner, _ := json.Marshal(map[string]any{
			"ocr_text": `line one\nline two\nline three`,
			"summary":  []string{`bullet\nwith escaped newline`, "plain bullet"},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{"text": string(inner)}},
				},
				"finishReason": "STOP",
			}},
		})
	})
	res, err := c.Process(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.OCR, `\n`) {
		t.Errorf("OCR still contains literal \\n: %q", res.OCR)
	}
	if strings.Count(res.OCR, "\n") != 2 {
		t.Errorf("expected 2 real newlines in OCR, got: %q", res.OCR)
	}
	if strings.Contains(res.Summary[0], `\n`) {
		t.Errorf("summary[0] still contains literal \\n: %q", res.Summary[0])
	}
	if res.Summary[1] != "plain bullet" {
		t.Errorf("plain bullet got mangled: %q", res.Summary[1])
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "429 rate limited",
			err:  &APIError{StatusCode: 429, Message: "rate limited"},
			want: true,
		},
		{
			name: "500 internal server error",
			err:  &APIError{StatusCode: 500, Message: "internal server error"},
			want: true,
		},
		{
			name: "503 service unavailable",
			err:  &APIError{StatusCode: 503, Message: "service unavailable"},
			want: true,
		},
		{
			name: "401 unauthorized",
			err:  &APIError{StatusCode: 401, Message: "API key invalid"},
			want: false,
		},
		{
			name: "403 forbidden",
			err:  &APIError{StatusCode: 403, Message: "access denied"},
			want: false,
		},
		{
			name: "400 bad request",
			err:  &APIError{StatusCode: 400, Message: "invalid request"},
			want: false,
		},
		{
			name: "network error (no HTTP status)",
			err:  errors.New("connection refused"),
			want: true,
		},
		{
			name: "unknown error",
			err:  errors.New("something completely unknown"),
			want: true,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsRetryable(tc.err)
			if got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
