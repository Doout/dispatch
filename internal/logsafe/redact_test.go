package logsafe

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func TestKnownSecretsAndEncodedCopies(t *testing.T) {
	secret := "build-token+with/slashes?and=padding"
	values := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret)), url.QueryEscape(secret)}
	values = append(values, base64.StdEncoding.EncodeToString([]byte(values[1])))
	for _, value := range values {
		if got := Text("output "+value, secret); strings.Contains(got, value) || !strings.Contains(got, "[redacted]") {
			t.Fatalf("credential was not filtered: %q", got)
		}
	}
}

func TestWrappedBase64AndUnfinishedCredentials(t *testing.T) {
	secret := strings.Repeat("private-value", 12)
	encoded := base64.StdEncoding.EncodeToString([]byte(secret))
	var wrapped []string
	for start := 0; start < len(encoded); start += 76 {
		wrapped = append(wrapped, encoded[start:min(start+76, len(encoded))])
	}
	got := Text(strings.Join(wrapped, "\n"), secret)
	for _, part := range wrapped {
		if len(part) >= 4 && strings.Contains(got, part) {
			t.Fatal("wrapped credential escaped")
		}
	}
	if got := New("private-value").Final("output private-"); strings.Contains(got, "private-") {
		t.Fatal("unfinished credential escaped")
	}
}

func TestCredentialPatternsAndOrdinaryBuildOutput(t *testing.T) {
	tests := map[string]string{
		`PASSWORD = "private-value"`:                                              "private-value",
		`{"api_key":"private-value"}`:                                             "private-value",
		`Authorization: Bearer private-value`:                                     "private-value",
		`postgresql://user:private-value@database/app`:                            "private-value",
		`ghs_123456789012345678901234567890`:                                      "ghs_123456789012345678901234567890",
		`eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.signaturesignature`:                 "eyJhbGci",
		base64.StdEncoding.EncodeToString([]byte(`{"password":"private-value"}`)): "eyJw",
	}
	for input, private := range tests {
		if got := Text(input); strings.Contains(got, private) {
			t.Fatalf("credential exposed in %q: %q", input, got)
		}
	}
	ordinary := "Step 14/20: cached\nimage sha256:" + strings.Repeat("a", 64) + "\nYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4\n"
	if got := Text(ordinary); got != ordinary {
		t.Fatalf("ordinary output changed: %q", got)
	}
}

func TestPrivateKeyAcrossLinesAndTerminalEscapes(t *testing.T) {
	f := New()
	for _, line := range []string{"-----BEGIN OPENSSH PRIVATE KEY-----", "private-body", "-----END OPENSSH PRIVATE KEY-----"} {
		if got := f.Line(line); got != "[redacted private key]" {
			t.Fatalf("private key line exposed: %q", got)
		}
	}
	if got := f.Line("build finished"); got != "build finished" {
		t.Fatal(got)
	}
	if got := Text("pass\x1b[31mword=private-value\x1b[0m"); strings.Contains(got, "private-value") {
		t.Fatal(got)
	}
}
