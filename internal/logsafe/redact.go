// Package logsafe filters command output before it leaves the installation.
package logsafe

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)
var credentials = regexp.MustCompile(`(?i)((?:password|passwd|passphrase|pwd|secret|token|api[_-]?key|authorization|client[_-]?secret|access[_-]?key|private[_-]?key|"auth")\s*["']?\s*[:=]\s*).*`)
var urlPassword = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*)://[^\s/@]+:[^\s/@]+@`)
var tokens = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|sk_(?:live_|test_)?[A-Za-z0-9_-]{16,}|cfut_[A-Za-z0-9_-]{16,}|xox[baprs]-[A-Za-z0-9-]{16,}|(?:AKIA|ASIA)[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)`)
var encoded = regexp.MustCompile(`[A-Za-z0-9+/_-]{20,}={0,2}`)

type Filter struct {
	known      []string
	privateKey bool
}

func New(secrets ...string) *Filter {
	known := map[string]bool{}
	add := func(value string) {
		known[value] = true
		for _, part := range strings.Split(value, "\n") {
			if len(part) >= 4 {
				known[part] = true
			}
		}
		// base64 tools commonly wrap output at 64 or 76 columns.
		for _, width := range []int{64, 76} {
			if len(value) <= width {
				continue
			}
			for start := 0; start < len(value); start += width {
				part := value[start:min(start+width, len(value))]
				if len(part) >= 4 {
					known[part] = true
				}
			}
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		add(secret)
		if len(secret) < 4 {
			continue
		}
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			value := encoding.EncodeToString([]byte(secret))
			add(value)
			add(encoding.EncodeToString([]byte(value)))
		}
		add(hex.EncodeToString([]byte(secret)))
		add(url.QueryEscape(secret))
		quoted, _ := json.Marshal(secret)
		add(string(quoted[1 : len(quoted)-1]))
	}
	f := &Filter{}
	for value := range known {
		f.known = append(f.known, value)
	}
	sort.Slice(f.known, func(i, j int) bool { return len(f.known[i]) > len(f.known[j]) })
	return f
}

func (f *Filter) Final(line string) string {
	for _, value := range f.known {
		for n := min(len(value)-1, len(line)); n >= 4; n-- {
			if strings.HasSuffix(line, value[:n]) {
				line = line[:len(line)-n] + "[redacted]"
				break
			}
		}
	}
	return f.Line(line)
}

// Line must receive complete lines. Keeping an unfinished line private prevents
// a token split across process writes or HTTP pages from escaping the filter.
func (f *Filter) Line(line string) string {
	line = ansi.ReplaceAllString(line, "")
	line = strings.Map(func(r rune) rune {
		if r < 32 && r != '\t' || r == 127 {
			return -1
		}
		return r
	}, line)
	if strings.Contains(line, "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY-----") {
		f.privateKey = true
	}
	if f.privateKey {
		if strings.Contains(line, "-----END ") && strings.Contains(line, "PRIVATE KEY-----") {
			f.privateKey = false
		}
		return "[redacted private key]"
	}
	for _, value := range f.known {
		line = strings.ReplaceAll(line, value, "[redacted]")
	}
	line = encoded.ReplaceAllStringFunc(line, func(value string) string {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err := encoding.DecodeString(value)
			if err != nil {
				continue
			}
			text := string(decoded)
			if credentials.MatchString(text) || tokens.MatchString(text) || urlPassword.MatchString(text) || strings.Contains(text, "PRIVATE KEY-----") {
				return "[redacted encoded credential]"
			}
			for _, known := range f.known {
				if strings.Contains(text, known) {
					return "[redacted encoded credential]"
				}
			}
		}
		return value
	})
	line = tokens.ReplaceAllString(line, "[redacted]")
	line = urlPassword.ReplaceAllString(line, "$1://[redacted]@")
	line = credentials.ReplaceAllString(line, "${1}[redacted]")
	return line
}

func Text(text string, secrets ...string) string {
	f := New(secrets...)
	lines := strings.Split(strings.ToValidUTF8(text, "�"), "\n")
	for i, line := range lines {
		lines[i] = f.Line(line)
	}
	return strings.Join(lines, "\n")
}
