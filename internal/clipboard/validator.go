package clipboard

import (
	"net/url"
	"strings"

	"github.com/atotto/clipboard"
)

// Validator checks and extracts valid downloadable URLs from text
type Validator struct {
	allowedSchemes map[string]bool
}

// NewValidator creates a new URL validator
func NewValidator() *Validator {
	return &Validator{
		allowedSchemes: map[string]bool{"http": true, "https": true},
	}
}

// ExtractURL validates and returns a clean URL, or empty string if invalid
func (v *Validator) ExtractURL(text string) string {
	text = strings.TrimSpace(text)

	// Quick reject: too long, contains newlines, or obviously not a URL
	if len(text) > 2048 || strings.ContainsAny(text, "\n\r") {
		return ""
	}

	// Must start with http:// or https://
	if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		return ""
	}

	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" || !v.allowedSchemes[parsed.Scheme] {
		return ""
	}

	return parsed.String()
}

// ReadURL reads the clipboard and returns a valid URL if found, or empty string otherwise
func ReadURL() string {
	text, err := clipboard.ReadAll()
	if err != nil {
		return ""
	}
	validator := NewValidator()
	return validator.ExtractURL(text)
}
