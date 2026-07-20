package ollama

import "time"

// ClientConfig configures an Ollama-backed ai.Provider.
type ClientConfig struct {
	// BaseURL is the Ollama server base URL. An empty value defaults to the
	// standard local daemon endpoint.
	BaseURL       string
	Model         string
	Timeout       time.Duration
	OCRPrompt     string
	SummaryPrompt string
}
