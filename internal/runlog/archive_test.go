package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFullArchiveAndSplitCredentials(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	w, err := Open(directory, "build", "never-publish-this")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("output never-publish-"))
	if text, _, _, err := Read(directory, "build", 0); err != nil || text != "" {
		t.Fatalf("unfinished credential escaped: %q %v", text, err)
	}
	_, _ = w.Write([]byte("this\n"))
	line := strings.Repeat("building image ", 100) + "\n"
	for i := 0; i < 2000; i++ {
		_, _ = w.Write([]byte(line))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var full strings.Builder
	var cursor int64
	for {
		text, next, incomplete, err := Read(directory, "build", cursor)
		if err != nil || incomplete {
			t.Fatalf("archive failure: %v %v", err, incomplete)
		}
		full.WriteString(text)
		if next == cursor {
			break
		}
		cursor = next
	}
	if full.Len() < 2<<20 || strings.Contains(full.String(), "never-publish") || strings.Count(full.String(), "building image ") != 200000 {
		t.Fatalf("missing or unsafe full log: %d bytes", full.Len())
	}
	info, _ := os.Stat(filepath.Join(directory, "build.log"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive permissions")
	}
}

func TestArchiveBoundsRetentionAndFailedDelivery(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	w, err := Open(directory, "build")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(strings.Repeat("x", pageSize+1)))
	_, _ = w.Write([]byte("\nnext line\n"))
	w.bytes = MaxBytes
	_, _ = w.Write([]byte("too much\n"))
	if err := w.Close(); err == nil {
		t.Fatal("reported an overflowing archive as complete")
	}
	text, _, incomplete, err := Read(directory, "build", 0)
	if err != nil || !incomplete || strings.Contains(text, strings.Repeat("x", 20)) || !strings.Contains(text, "next line") {
		t.Fatalf("unsafe or incomplete log: %q %v %v", text, incomplete, err)
	}
	for _, id := range []string{"../key", "/tmp/file", ""} {
		if _, err := Open(directory, id); err == nil {
			t.Fatal("accepted invalid archive ID")
		}
	}
	old := time.Now().Add(-Retention - time.Hour)
	_ = os.Chtimes(filepath.Join(directory, "build.log"), old, old)
	if err := Prune(directory, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "build.log")); !os.IsNotExist(err) {
		t.Fatal("expired archive retained")
	}
}
