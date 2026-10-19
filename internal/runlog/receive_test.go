package runlog

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLogTransferRetriesAndRejectsGapsSecretsAndChangedPrefixes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	first, second := "first line\n", "second line\n"
	if err := Receive(directory, "job", 0, first); err != nil {
		t.Fatal(err)
	}
	if err := Receive(directory, "job", int64(len(first)), second); err != nil {
		t.Fatal(err)
	}
	if err := Receive(directory, "job", 0, first); err != nil {
		t.Fatalf("retry after response loss: %v", err)
	}
	for _, test := range []struct {
		offset int64
		text   string
	}{
		{0, "changed\n"}, {999, "gap\n"}, {int64(len(first) + len(second)), "PASSWORD=private-value\n"}, {0, "unfinished"},
	} {
		if err := Receive(directory, "job", test.offset, test.text); err == nil {
			t.Fatalf("unsafe transfer accepted: %+v", test)
		}
	}
	if err := Finish(directory, "job", 100); err == nil {
		t.Fatal("incomplete transfer was marked complete")
	}
	if err := Finish(directory, "job", int64(len(first)+len(second))); err != nil {
		t.Fatal(err)
	}
	text, _, incomplete, err := Read(directory, "job", 0)
	if err != nil || incomplete || text != first+second || !Complete(directory, "job") {
		t.Fatalf("full transfer changed: %q %v", text, err)
	}
}

func TestStreamWithholdsPartialLinesAndBatchesChunks(t *testing.T) {
	var chunks []string
	w := Stream(func(text string) error { chunks = append(chunks, text); return nil }, "private-value")
	_, _ = w.Write([]byte("output private-"))
	w.Flush()
	if len(chunks) != 0 {
		t.Fatal("partial secret emitted")
	}
	_, _ = w.Write([]byte("value\n"))
	w.Flush()
	if len(chunks) != 1 || strings.Contains(chunks[0], "private") {
		t.Fatalf("split secret escaped: %v", chunks)
	}
	_, _ = w.Write([]byte("done"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(chunks, "") != "output [redacted]\ndone\n" {
		t.Fatal(chunks)
	}
}
