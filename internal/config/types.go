package config

import "time"

type Config struct {
	ListenAddr string `toml:"listen_addr" json:"listen_addr"`
	WebDAVUser string `toml:"webdav_user" json:"webdav_user"`
	WebDAVPass string `toml:"webdav_pass" json:"webdav_pass"`

	TemplateDir    string `toml:"template_dir" json:"template_dir"`
	VaultDir       string `toml:"vault_dir" json:"vault_dir"`
	DefaultPDFDir  string `toml:"default_pdf_dir" json:"default_pdf_dir"`
	DefaultNoteDir string `toml:"default_note_dir" json:"default_note_dir"`
	StateFile      string `toml:"state_file" json:"state_file"`

	Gemini   GeminiConfig   `toml:"gemini" json:"gemini"`
	Upstream UpstreamConfig `toml:"upstream" json:"upstream"`
	Routes   []Route        `toml:"route" json:"route"`
}

type UpstreamConfig struct {
	URL          string `toml:"url" json:"url"`
	Prefix       string `toml:"prefix" json:"prefix"`
	User         string `toml:"user" json:"user"`
	PasswordFile string `toml:"password_file" json:"password_file"`

	Password string `toml:"-" json:"-"`
}

type Route struct {
	From     string `toml:"from" json:"from"`
	PDFDir   string `toml:"pdf_dir" json:"pdf_dir"`
	NoteDir  string `toml:"note_dir" json:"note_dir"`
	NoteName string `toml:"note_name" json:"note_name"`
	PDFName  string `toml:"pdf_name" json:"pdf_name"`
	Template string `toml:"template" json:"template"`
	AI       bool   `toml:"ai" json:"ai"`
}

type GeminiConfig struct {
	APIKeyFile    string `toml:"api_key_file" json:"api_key_file"`
	Model         string `toml:"model" json:"model"`
	Timeout       string `toml:"timeout" json:"timeout"`
	OCRPrompt     string `toml:"ocr_prompt" json:"ocr_prompt"`
	SummaryPrompt string `toml:"summary_prompt" json:"summary_prompt"`

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
