package gemini

import "time"

// ClientConfig configures a Gemini-backed ai.Provider.
type ClientConfig struct {
	// Endpoint is the API base URL. Tests override this; production leaves it
	// empty and the client defaults to https://generativelanguage.googleapis.com.
	Endpoint      string
	APIKey        string
	Model         string
	Timeout       time.Duration
	OCRPrompt     string
	SummaryPrompt string
}
