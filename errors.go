package systemone

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalidRequest marks a request rejected before it is sent.
	ErrInvalidRequest = errors.New("systemone: invalid request")
	// ErrInvalidResponse marks a successful response whose body does not match
	// the wire format.
	ErrInvalidResponse = errors.New("systemone: invalid response")
)

const maxErrorBodyLength = 200

// APIError is an unsuccessful HTTP response.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Message is the server's error message, or the start of the body when it
	// carries none.
	Message string
	// Body is the raw response body.
	Body []byte
	// RequestID is the x-typesafe-request-id header, empty when absent.
	RequestID string
	// RetryAfter is the wait the server requested, zero when absent.
	RetryAfter time.Duration
	// Endpoint is the request method and URL.
	Endpoint string

	retryAfterSet bool
}

func (e *APIError) Error() string {
	var b strings.Builder

	b.WriteString("systemone: ")
	b.WriteString(e.Endpoint)
	b.WriteString(": ")
	b.WriteString(strconv.Itoa(e.StatusCode))

	if e.Message != "" {
		b.WriteByte(' ')
		b.WriteString(e.Message)
	}

	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request_id=%s)", e.RequestID)
	}

	return b.String()
}

// errorMessage finds the server's message in an error body, accepting the
// error, message, and detail shapes the API and its validation layer emit.
func errorMessage(body []byte) string {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return truncate(strings.TrimSpace(string(body)))
	}

	switch v := decoded.(type) {
	case string:
		return v
	case map[string]any:
		if message := objectMessage(v); message != "" {
			return message
		}
	}

	return truncate(string(body))
}

func objectMessage(body map[string]any) string {
	if message, ok := body["error"].(string); ok {
		return message
	}

	if errorObject, ok := body["error"].(map[string]any); ok {
		if message, ok := errorObject["message"].(string); ok {
			return message
		}
	}

	if message, ok := body["message"].(string); ok {
		return message
	}

	switch detail := body["detail"].(type) {
	case string:
		return detail
	case map[string]any:
		if message, ok := detail["message"].(string); ok {
			return message
		}
	case []any:
		return validationMessage(detail)
	}

	return ""
}

func validationMessage(detail []any) string {
	parts := make([]string, 0, len(detail))

	for _, entry := range detail {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		message, ok := item["msg"].(string)
		if !ok {
			continue
		}

		if path := locationPath(item["loc"]); path != "" {
			message = path + ": " + message
		}

		parts = append(parts, message)
	}

	return strings.Join(parts, "; ")
}

func locationPath(location any) string {
	segments, ok := location.([]any)
	if !ok {
		return ""
	}

	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "body" {
			continue
		}

		parts = append(parts, fmt.Sprint(segment))
	}

	return strings.Join(parts, ".")
}

func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxErrorBodyLength {
		return s
	}

	return string([]rune(s)[:maxErrorBodyLength]) + "…"
}
