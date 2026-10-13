// Package sourceerror describes repository failures without exposing upstream pages.
package sourceerror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type HTTPError struct {
	StatusCode               int
	Status, Message          string
	Maintenance, RateLimited bool
	RetryAt                  time.Time
}

func (e *HTTPError) Error() string {
	if e.Status == "" {
		eStatus := fmt.Sprintf("%d %s", e.StatusCode, http.StatusText(e.StatusCode))
		if e.Message == "" {
			return "GitHub returned " + eStatus
		}
		return fmt.Sprintf("GitHub returned %s: %s", eStatus, e.Message)
	}
	if e.Message == "" {
		return "GitHub returned " + e.Status
	}
	return fmt.Sprintf("GitHub returned %s: %s", e.Status, e.Message)
}
func FromResponse(response *http.Response) *HTTPError {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	lower := strings.ToLower(string(body))
	e := &HTTPError{StatusCode: response.StatusCode, Status: response.Status, Maintenance: response.StatusCode >= 500 && (strings.Contains(lower, "down for maintenance") || strings.Contains(lower, "scheduled maintenance"))}
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		e.Message = Concise(payload.Message)
	}
	e.RateLimited = response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(e.Message), "rate limit"))
	if value := response.Header.Get("Retry-After"); value != "" {
		var seconds int
		if _, err := fmt.Sscanf(value, "%d", &seconds); err == nil && seconds > 0 && seconds <= 86400 {
			e.RetryAt = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		} else if at, err := http.ParseTime(value); err == nil {
			e.RetryAt = at
		}
	}
	if e.RateLimited && e.RetryAt.IsZero() {
		var epoch int64
		if _, err := fmt.Sscanf(response.Header.Get("X-RateLimit-Reset"), "%d", &epoch); err == nil {
			e.RetryAt = time.Unix(epoch, 0).UTC()
		}
	}
	return e
}

type Failure struct {
	Message   string
	Transient bool
	RetryAt   time.Time
}

func Describe(err error) Failure {
	if err == nil {
		return Failure{}
	}
	provider := "Repository service"
	if strings.Contains(strings.ToLower(err.Error()), "github") {
		provider = "GitHub"
	}
	return describe(err, provider)
}
func describe(err error, provider string) Failure {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var selected Failure
		for _, child := range joined.Unwrap() {
			if child == nil {
				continue
			}
			f := describe(child, provider)
			// A permanent failure must not be hidden by a temporary outage.
			if selected.Message == "" || selected.Transient && !f.Transient {
				selected = f
			}
			if f.RetryAt.After(selected.RetryAt) {
				selected.RetryAt = f.RetryAt
			}
		}
		return selected
	}
	if child := errors.Unwrap(err); child != nil {
		f := describe(child, provider)
		var response *HTTPError
		if !f.Transient && f.Message == Concise(child.Error()) && !errors.As(child, &response) {
			return describeText(err.Error(), provider)
		}
		return f
	}
	var temporary *TemporaryError
	if errors.As(err, &temporary) {
		return Failure{Message: temporary.Message, Transient: true, RetryAt: temporary.RetryAt}
	}
	var response *HTTPError
	if errors.As(err, &response) {
		f := Failure{RetryAt: response.RetryAt}
		switch {
		case response.Maintenance:
			f.Message = "GitHub is down for maintenance."
			f.Transient = true
		case response.RateLimited:
			f.Message = "GitHub's request limit has been reached."
			f.Transient = true
		case response.StatusCode >= 500:
			f.Message = "GitHub is temporarily unavailable."
			f.Transient = true
		case response.StatusCode == 408:
			f.Message = "GitHub did not respond in time."
			f.Transient = true
		case response.StatusCode == 401:
			f.Message = "GitHub authentication failed. Check the connection credentials."
		case response.StatusCode == 403:
			f.Message = "GitHub access was denied. Check the App's repository permissions."
		case response.StatusCode == 404:
			f.Message = "GitHub could not find the requested resource. Check its name and App access."
		default:
			f.Message = Concise(err.Error())
		}
		return f
	}
	if errors.Is(err, context.Canceled) {
		return Failure{Message: "Polling was cancelled."}
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return Failure{Message: provider + " did not respond in time.", Transient: true}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return Failure{Message: "The repository host could not be resolved. Check its address and DNS settings."}
	}
	return describeText(err.Error(), provider)
}

func describeText(message, provider string) Failure {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "rate limit exceeded") || strings.Contains(lower, "request limit has been reached") || strings.Contains(lower, "returned 429"):
		return Failure{Message: "GitHub's request limit has been reached.", Transient: true}
	case strings.Contains(lower, "returned 401"):
		return Failure{Message: "GitHub authentication failed. Check the connection credentials."}
	case strings.Contains(lower, "returned 403"):
		return Failure{Message: "GitHub access was denied. Check the App's repository permissions."}
	case strings.Contains(lower, "returned 404"):
		return Failure{Message: "GitHub could not find the requested resource. Check its name and App access."}
	case strings.Contains(lower, "scheduled maintenance") || strings.Contains(lower, "down for maintenance"):
		return Failure{Message: provider + " is down for maintenance.", Transient: true}
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout") || strings.Contains(lower, "connection timed out") || strings.Contains(lower, "did not respond in time"):
		return Failure{Message: provider + " did not respond in time.", Transient: true}
	case strings.Contains(lower, "returned 503") || strings.Contains(lower, "returned 502") || strings.Contains(lower, "returned 504") || strings.Contains(lower, "service temporarily unavailable") || strings.Contains(lower, "is temporarily unavailable") || strings.Contains(lower, "returned error: 503") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") || message == io.EOF.Error() || message == io.ErrUnexpectedEOF.Error():
		return Failure{Message: provider + " is temporarily unavailable.", Transient: true}
	case strings.Contains(lower, "permission denied (publickey)") || strings.Contains(lower, "authentication failed"):
		return Failure{Message: "Repository access was denied. Check the SSH key or GitHub App credentials."}
	}
	return Failure{Message: Concise(message)}
}

var urlPattern = regexp.MustCompile(`https?://[^\s"<>]+`)

func Concise(message string) string {
	// HTML error pages are not diagnostics. Never render or store their contents.
	if at := strings.Index(message, "<"); at >= 0 {
		message = message[:at]
	}
	seen := map[string]bool{}
	lines := []string{}
	for _, line := range strings.Split(message, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	message = strings.Join(lines, " ")
	message = urlPattern.ReplaceAllStringFunc(message, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[repository URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
	if len(message) > 512 {
		message = string([]rune(message)[:min(len([]rune(message)), 512)])
	}
	if message == "" {
		message = "The repository request failed."
	}
	return message
}

type TemporaryError struct {
	Message string
	RetryAt time.Time
}

func (e *TemporaryError) Error() string { return e.Message }

// SetPollFailure marks only temporary failures for scheduled retry. Permanent
// errors retain their failed outcome and configuration-specific recovery advice.
func SetPollFailure(item *core.EventActivity, err error, interval time.Duration) {
	f := Describe(err)
	item.State = "failed"
	item.Message = f.Message
	item.NextAttemptAt = nil
	if f.Transient {
		at := item.CreatedAt.Add(interval)
		if f.RetryAt.After(at) {
			at = f.RetryAt
		}
		item.NextAttemptAt = &at
	}
}

// Normalize also handles errors saved by controllers before structured failures.
func Normalize(message string) string {
	if message == "" {
		return ""
	}
	return Describe(errors.New(message)).Message
}
