package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/automationclient"
)

func TestCLIJSONAndExplicitConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	os.WriteFile(path, []byte("dsa_cli_fixture"), 0600)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer dsa_cli_fixture" {
			t.Error("lost credential")
		}
		io.WriteString(w, `[{"id":"p-1"}]`)
	}))
	defer server.Close()
	var out, errout bytes.Buffer
	prefix := []string{"--url", server.URL, "--token-file", path}
	code := run(context.Background(), append(prefix, "projects", "list"), strings.NewReader(""), &out, &errout)
	var result automationclient.Result
	if json.Unmarshal(out.Bytes(), &result) != nil || code != 0 || !result.OK || requests != 1 {
		t.Fatal("CLI did not return JSON", code, out.String())
	}
	out.Reset()
	code = run(context.Background(), append(prefix, "server", "delete", "--server", "server-1", "--key", "stable-key", "--input", "-"), strings.NewReader(`{"digest":"x"}`), &out, &errout)
	if code != 2 || requests != 1 || strings.Contains(out.String(), "dsa_cli_fixture") {
		t.Fatal("CLI invented confirmation", code, out.String())
	}
}

func TestMCPConfigurationFailureKeepsStdoutProtocolOnly(t *testing.T) {
	var out, errout bytes.Buffer
	code := run(context.Background(), []string{"--url", "https://example.com", "--token-file", filepath.Join(t.TempDir(), "missing"), "mcp"}, strings.NewReader(""), &out, &errout)
	if code != 2 || out.Len() != 0 || errout.Len() == 0 {
		t.Fatal("MCP config wrote non-protocol output")
	}
}
