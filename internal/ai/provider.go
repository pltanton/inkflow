// Package ai defines the contract inkflow's importer uses to talk to an
// external AI backend that turns an uploaded PDF into a transcription
// and a short summary.
package ai

import (
	"context"
	"io"
)

// Provider runs OCR + summary on a PDF and returns the structured result.
// Implementations must be safe for concurrent use.
type Provider interface {
	Process(ctx context.Context, pdf io.Reader) (Result, error)
}

// Result is the structured output of a Provider call.
type Result struct {
	OCR     string
	Summary []string
}
