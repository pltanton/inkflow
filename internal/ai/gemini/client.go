package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"inkflow/internal/ai"
)

const defaultEndpoint = "https://generativelanguage.googleapis.com"

// APIError is returned by Client.Process for non-2xx HTTP responses from the
// Gemini API. It carries the raw status code so callers can classify the error
// (e.g. IsRetryable) without parsing the message string.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini %d: %s", e.StatusCode, e.Message)
}

// IsRetryable reports whether an error returned by Client.Process is worth
// retrying. Rules:
//   - 429 (rate limited) or any 5xx (server error) → retryable
//   - 400, 401, 403, 404 → non-retryable (permanent client/auth error)
//   - nil → false (not an error)
//   - anything else (network, timeout, unknown) → retryable (fail-open)
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 400, 401, 403, 404:
			return false
		}
		return true // 429, 500, 502, 503, 504, other HTTP
	}
	return true // network / timeout / unknown — assume transient
}

// Compile-time interface compliance check.
var _ ai.Provider = (*Client)(nil)

// Client implements ai.Provider against the Gemini generateContent endpoint.
type Client struct {
	cfg        ClientConfig
	httpClient *http.Client
}

// New builds a Client. The returned value is safe for concurrent use.
func New(cfg ClientConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

// Process sends pdf to Gemini and returns the parsed result.
func (c *Client) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	pdfData, err := io.ReadAll(pdf)
	if err != nil {
		return ai.Result{}, fmt.Errorf("read pdf: %w", err)
	}

	body, err := json.Marshal(c.buildRequest(pdfData))
	if err != nil {
		return ai.Result{}, fmt.Errorf("encode request: %w", err)
	}

	endpoint := c.cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent",
		strings.TrimRight(endpoint, "/"), c.cfg.Model)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ai.Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Auth via header rather than ?key= query param: keeps the API key out of
	// any URL that net/http includes in transport errors, which the importer
	// would otherwise write verbatim into the Obsidian note.
	req.Header.Set("X-Goog-Api-Key", c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ai.Result{}, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.Result{}, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ai.Result{}, &APIError{
			StatusCode: resp.StatusCode,
			Message:    extractErrorMessage(respBody),
		}
	}

	return parseResponse(respBody)
}

func (c *Client) buildRequest(pdfData []byte) map[string]any {
	prompt := strings.TrimSpace(c.cfg.OCRPrompt) + "\n\n" +
		strings.TrimSpace(c.cfg.SummaryPrompt)

	return map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]any{
				{"text": prompt},
				{"inline_data": map[string]any{
					"mime_type": "application/pdf",
					"data":      base64.StdEncoding.EncodeToString(pdfData),
				}},
			},
		}},
		"generationConfig": map[string]any{
			"response_mime_type": "application/json",
			"response_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ocr_text": map[string]any{"type": "string"},
					"summary": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
				"required": []string{"ocr_text", "summary"},
			},
		},
	}
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
}

type innerPayload struct {
	OCRText string   `json:"ocr_text"`
	Summary []string `json:"summary"`
}

func parseResponse(body []byte) (ai.Result, error) {
	var outer generateResponse
	if err := json.Unmarshal(body, &outer); err != nil {
		return ai.Result{}, fmt.Errorf("parse outer response: %w", err)
	}
	if len(outer.Candidates) == 0 || len(outer.Candidates[0].Content.Parts) == 0 {
		return ai.Result{}, fmt.Errorf("parse response: no candidates")
	}
	candidate := outer.Candidates[0]
	if reason := candidate.FinishReason; reason != "" && reason != "STOP" {
		return ai.Result{}, fmt.Errorf("gemini stopped with reason %q; response may be truncated", reason)
	}
	raw := candidate.Content.Parts[0].Text
	var inner innerPayload
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return ai.Result{}, fmt.Errorf("parse JSON-mode payload: %w", err)
	}
	summary := make([]string, len(inner.Summary))
	for i, s := range inner.Summary {
		summary[i] = unescapeNewlines(s)
	}
	return ai.Result{OCR: unescapeNewlines(inner.OCRText), Summary: summary}, nil
}

// unescapeNewlines repairs Gemini's JSON-mode output when the model
// double-escapes newlines (writes `\\n` in its JSON, which parses to two
// literal characters `\` and `n` instead of a real newline). Handwritten
// notes essentially never contain a literal backslash-n, so converting
// every occurrence is a safe defensive fix.
func unescapeNewlines(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}

// extractErrorMessage pulls error.message out of a Gemini error body so the
// importer doesn't paste 30 lines of JSON into the note. Falls back to the
// trimmed raw body if parsing fails or the field is missing.
func extractErrorMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return strings.TrimSpace(string(body))
}
