// Package ollama implements the AI provider contract against a local Ollama
// server's non-streaming chat API.
package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"inkflow/internal/ai"
)

const defaultBaseURL = "http://localhost:11434"

// Compile-time interface compliance check.
var _ ai.Provider = (*Client)(nil)

// Client implements ai.Provider using Ollama's /api/chat endpoint.
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

// Process sends the PDF bytes as the vision input and parses Ollama's
// structured non-streaming chat response.
func (c *Client) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	pdfData, err := io.ReadAll(pdf)
	if err != nil {
		return ai.Result{}, fmt.Errorf("read pdf: %w", err)
	}

	body, err := json.Marshal(c.buildRequest(pdfData))
	if err != nil {
		return ai.Result{}, fmt.Errorf("encode request: %w", err)
	}

	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return ai.Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ai.Result{}, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.Result{}, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ai.Result{}, fmt.Errorf("ollama %w", &ai.APIError{
			StatusCode: resp.StatusCode,
			Message:    extractErrorMessage(respBody),
		})
	}

	return parseResponse(respBody)
}

func (c *Client) buildRequest(pdfData []byte) map[string]any {
	prompt := strings.TrimSpace(c.cfg.OCRPrompt) + "\n\n" + strings.TrimSpace(c.cfg.SummaryPrompt)
	return map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]any{{
			"role":    "user",
			"content": prompt,
			"images":  []string{base64.StdEncoding.EncodeToString(pdfData)},
		}},
		"stream": false,
		"format": map[string]any{
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
	}
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type resultPayload struct {
	OCRText *string   `json:"ocr_text"`
	Summary *[]string `json:"summary"`
}

func parseResponse(body []byte) (ai.Result, error) {
	var outer chatResponse
	if err := json.Unmarshal(body, &outer); err != nil {
		return ai.Result{}, fmt.Errorf("parse outer response: %w", err)
	}
	if outer.Message.Content == "" {
		return ai.Result{}, fmt.Errorf("parse response: no message content")
	}
	var payload resultPayload
	if err := json.Unmarshal([]byte(outer.Message.Content), &payload); err != nil {
		return ai.Result{}, fmt.Errorf("parse JSON-mode payload: %w", err)
	}
	if payload.OCRText == nil || payload.Summary == nil {
		return ai.Result{}, fmt.Errorf("parse JSON-mode payload: missing ocr_text or summary")
	}
	summary := make([]string, len(*payload.Summary))
	for i, item := range *payload.Summary {
		summary[i] = unescapeNewlines(item)
	}
	return ai.Result{OCR: unescapeNewlines(*payload.OCRText), Summary: summary}, nil
}

func unescapeNewlines(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}

func extractErrorMessage(body []byte) string {
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && len(parsed.Error) > 0 {
		var message string
		if json.Unmarshal(parsed.Error, &message) == nil && message != "" {
			return message
		}
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(parsed.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
	}
	return strings.TrimSpace(string(body))
}
