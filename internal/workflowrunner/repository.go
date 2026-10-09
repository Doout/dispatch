package workflowrunner

import (
	"net/url"
	"strings"
)

// ValidRepositoryURL permits remote Git transports without helper protocols,
// local paths, command options, or passwords embedded in a URL.
func ValidRepositoryURL(raw string) bool {
	if strings.ContainsAny(raw, "\x00\r\n\t ") {
		return false
	}
	if parsed, err := url.Parse(raw); err == nil && (parsed.Scheme == "https" || parsed.Scheme == "ssh") && parsed.Hostname() != "" && parsed.RawQuery == "" && parsed.Fragment == "" {
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password || strings.HasPrefix(parsed.User.Username(), "-") {
				return false
			}
		}
		return !strings.HasPrefix(parsed.Hostname(), "-")
	}
	if strings.Contains(raw, "::") {
		return false
	}
	user, rest, ok := strings.Cut(raw, "@")
	if !ok || user == "" || strings.HasPrefix(user, "-") || strings.ContainsAny(user, "/:\\") {
		return false
	}
	host, path, ok := strings.Cut(rest, ":")
	return ok && host != "" && !strings.HasPrefix(host, "-") && !strings.ContainsAny(host, "/:\\@") && path != ""
}
