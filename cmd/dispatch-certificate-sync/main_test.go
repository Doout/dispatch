package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionAndMissingReloadCommand(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil || !strings.HasPrefix(out.String(), "Dispatch certificate sync ") {
		t.Fatal(out.String(), err)
	}
	if err := run(context.Background(), []string{"--origin", "https://alpha.dispatch.example.test"}, &out); err == nil {
		t.Fatal("installer accepted missing reload command")
	}
}
