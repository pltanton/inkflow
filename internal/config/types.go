package config

import (
	"path"
	"strings"
	"time"
)

type Config struct {
	ListenAddr                string        `toml:"listen_addr" json:"listen_addr"`
	WebDAVUser                string        `toml:"webdav_user" json:"webdav_user"`
	WebDAVPass                string        `toml:"webdav_pass" json:"webdav_pass"`
	ReadHeaderTimeout         string        `toml:"read_header_timeout" json:"read_header_timeout"`
	ReadTimeout               string        `toml:"read_timeout" json:"read_timeout"`
	WriteTimeout              string        `toml:"write_timeout" json:"write_timeout"`
	IdleTimeout               string        `toml:"idle_timeout" json:"idle_timeout"`
	MaxUploadBytes            int64         `toml:"max_upload_bytes" json:"max_upload_bytes"`
	ReadHeaderTimeoutDuration time.Duration `toml:"-" json:"-"`
	ReadTimeoutDuration       time.Duration `toml:"-" json:"-"`
	WriteTimeoutDuration      time.Duration `toml:"-" json:"-"`
	IdleTimeoutDuration       time.Duration `toml:"-" json:"-"`

	TemplateDir    string `toml:"template_dir" json:"template_dir"`
	VaultDir       string `toml:"vault_dir" json:"vault_dir"`
	DefaultPDFDir  string `toml:"default_pdf_dir" json:"default_pdf_dir"`
	DefaultNoteDir string `toml:"default_note_dir" json:"default_note_dir"`
	StateFile      string `toml:"state_file" json:"state_file"`

	AI            AIConfig            `toml:"ai" json:"ai"`
	Gemini        GeminiConfig        `toml:"gemini" json:"gemini"`
	OpenAI        OpenAIConfig        `toml:"openai" json:"openai"`
	Ollama        OllamaConfig        `toml:"ollama" json:"ollama"`
	Observability ObservabilityConfig `toml:"observability" json:"observability"`
	Routes        []Route             `toml:"route" json:"route"`
}

// ObservabilityConfig controls optional Prometheus metrics exposure.
// A dedicated metrics listener is intentionally unauthenticated; bind it to a
// private address or protect it at the network layer.
type ObservabilityConfig struct {
	MetricsEnabled bool   `toml:"metrics_enabled" json:"metrics_enabled"`
	MetricsAddr    string `toml:"metrics_addr" json:"metrics_addr"`
}

// NormalizeRoutePrefix returns the canonical form used for route matching.
func NormalizeRoutePrefix(prefix string) string {
	prefix = strings.ReplaceAll(prefix, "\\", "/")
	if prefix == "" {
		return ""
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return path.Clean(prefix) + "/"
}

type Route struct {
	From     string `toml:"from" json:"from"`
	PDFDir   string `toml:"pdf_dir" json:"pdf_dir"`
	NoteDir  string `toml:"note_dir" json:"note_dir"`
	NoteName string `toml:"note_name" json:"note_name"`
	PDFName  string `toml:"pdf_name" json:"pdf_name"`
	Template string `toml:"template" json:"template"`
	AI       bool   `toml:"ai" json:"ai"`
	// TagMergeStrategy is "merge" by default; "replace" retains the legacy
	// filename-authoritative tag behavior.
	TagMergeStrategy string `toml:"tag_merge_strategy" json:"tag_merge_strategy"`
	// Nil means the default true. A pointer distinguishes an omitted value from
	// an explicit TOML false.
	PreserveMarkerOnAIFailure *bool `toml:"preserve_marker_on_ai_failure" json:"preserve_marker_on_ai_failure"`
}

type GeminiConfig struct {
	APIKeyFile    string `toml:"api_key_file" json:"api_key_file"`
	Model         string `toml:"model" json:"model"`
	Timeout       string `toml:"timeout" json:"timeout"`
	OCRPrompt     string `toml:"ocr_prompt" json:"ocr_prompt"`
	SummaryPrompt string `toml:"summary_prompt" json:"summary_prompt"`
	// MinReprocessInterval, when set, suppresses AI re-processing for a
	// route/output-path match whose full SHA256 changed but whose last AI
	// run succeeded within this interval. Empty/"0s" disables debouncing
	// (default: disabled, preserving current behavior exactly).
	MinReprocessInterval         string        `toml:"min_reprocess_interval" json:"min_reprocess_interval"`
	MinReprocessIntervalDuration time.Duration `toml:"-" json:"-"`

	Retry RetryConfig `toml:"retry" json:"retry"`
}

// RetryConfig holds configuration for automatic AI retry on failure.
// BackoffDuration is populated by validate() from Backoff and is not a TOML field.
type RetryConfig struct {
	Enabled         bool          `toml:"enabled" json:"enabled"`
	MaxRetries      int           `toml:"max_retries" json:"max_retries"`
	Backoff         string        `toml:"backoff" json:"backoff"`
	BackoffDuration time.Duration `toml:"-" json:"-"`
}

type AIConfig struct {
	Provider string `toml:"provider" json:"provider"`
}

type OpenAIConfig struct {
	APIKeyFile    string `toml:"api_key_file" json:"api_key_file"`
	Model         string `toml:"model" json:"model"`
	Timeout       string `toml:"timeout" json:"timeout"`
	OCRPrompt     string `toml:"ocr_prompt" json:"ocr_prompt"`
	SummaryPrompt string `toml:"summary_prompt" json:"summary_prompt"`
}

// OllamaConfig configures a local Ollama vision model. Unlike cloud
// providers, Ollama does not require an API key.
type OllamaConfig struct {
	BaseURL       string `toml:"base_url" json:"base_url"`
	Model         string `toml:"model" json:"model"`
	Timeout       string `toml:"timeout" json:"timeout"`
	OCRPrompt     string `toml:"ocr_prompt" json:"ocr_prompt"`
	SummaryPrompt string `toml:"summary_prompt" json:"summary_prompt"`
}
