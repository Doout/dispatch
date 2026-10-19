// Package runlog holds a temporary, filtered copy for delivery to GitHub Actions.
package runlog

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/logsafe"
)

const MaxBytes = 64 << 20
const MaxDirectoryBytes = 1 << 30
const Retention = 24 * time.Hour
const pageSize = 64 << 10

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var quota = struct {
	sync.Mutex
	active map[string]int
}{active: map[string]int{}}

type Writer struct {
	mu        sync.Mutex
	file      *os.File
	filter    *logsafe.Filter
	pending   []byte
	bytes     int64
	err       error
	dropping  bool
	directory string
	closed    bool
	emit      func(string) error
	chunk     strings.Builder
}

func Open(directory, id string, secrets ...string) (*Writer, error) {
	if directory == "" || !idPattern.MatchString(id) {
		return nil, errors.New("log archive is not configured")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("log directory must be private")
	}
	quota.Lock()
	defer quota.Unlock()
	_ = Prune(directory, time.Now())
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var size int64
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	// Reserve a whole archive for active jobs. This is intentionally stricter
	// than the actual bytes written so concurrent jobs cannot fill the volume.
	if size+int64(quota.active[directory]+1)*MaxBytes > MaxDirectoryBytes {
		return nil, errors.New("temporary GitHub log storage is full")
	}
	f, err := os.OpenFile(filepath.Join(directory, id+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	quota.active[directory]++
	return &Writer{file: f, filter: logsafe.New(secrets...), directory: directory}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if w.err != nil {
		return n, nil
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if len(w.pending)+len(part) > pageSize {
			w.pending = nil
			w.dropping = true
		}
		if !w.dropping {
			w.pending = append(w.pending, part...)
		}
		if i < 0 {
			break
		}
		w.flushLine()
		p = p[i+1:]
	}
	return n, nil
}
func (w *Writer) flushLine() {
	line := "[line exceeded 64 KiB and was withheld]"
	if !w.dropping {
		line = w.filter.Line(string(w.pending))
	}
	w.pending, w.dropping = nil, false
	if w.bytes+int64(len(line)+1) > MaxBytes {
		w.err = errors.New("log archive exceeded 64 MiB")
		return
	}
	if w.emit != nil {
		w.chunk.WriteString(line + "\n")
		w.bytes += int64(len(line) + 1)
		if w.chunk.Len() >= 32<<10 {
			w.flushChunk()
		}
		return
	}
	n, err := io.WriteString(w.file, line+"\n")
	w.bytes += int64(n)
	w.err = err
}

func Stream(emit func(string) error, secrets ...string) *Writer {
	return &Writer{emit: emit, filter: logsafe.New(secrets...)}
}
func (w *Writer) flushChunk() {
	if w.emit != nil && w.err == nil && w.chunk.Len() > 0 {
		w.err = w.emit(w.chunk.String())
		w.chunk.Reset()
	}
}
func (w *Writer) Flush() { w.mu.Lock(); defer w.mu.Unlock(); w.flushChunk() }
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil && (len(w.pending) > 0 || w.dropping) {
		if !w.dropping {
			w.pending = []byte(w.filter.Final(string(w.pending)))
		}
		w.flushLine()
	}
	if w.emit != nil {
		w.flushChunk()
		return w.err
	}
	if w.err == nil {
		w.err = w.file.Sync()
	}
	if err := w.file.Close(); w.err == nil {
		w.err = err
	}
	quota.Lock()
	quota.active[w.directory]--
	if quota.active[w.directory] == 0 {
		delete(quota.active, w.directory)
	}
	quota.Unlock()
	if w.err != nil {
		_ = os.WriteFile(w.file.Name()+".failed", []byte("Log delivery is incomplete.\n"), 0600)
	} else {
		w.err = os.WriteFile(w.file.Name()+".done", nil, 0600)
	}
	return w.err
}

func Complete(directory, id string) bool {
	if directory == "" || !idPattern.MatchString(id) {
		return false
	}
	info, err := os.Stat(filepath.Join(directory, id+".log.done"))
	return err == nil && info.Mode().IsRegular()
}

func (w *Writer) Incomplete() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = errors.New("worker output did not include the full log")
	if w.file != nil {
		_ = os.WriteFile(w.file.Name()+".failed", []byte("Log delivery is incomplete.\n"), 0600)
	}
}

// Read returns complete filtered lines. A cursor is a byte offset into this
// immutable prefix; it never points into an unfiltered process buffer.
func Read(directory, id string, offset int64) (string, int64, bool, error) {
	if directory == "" || !idPattern.MatchString(id) || offset < 0 {
		return "", offset, false, errors.New("invalid log cursor")
	}
	path := filepath.Join(directory, id+".log")
	file, err := os.Open(path)
	if err != nil {
		return "", offset, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", offset, false, err
	}
	if offset > info.Size() {
		return "", offset, false, errors.New("invalid log cursor")
	}
	data := make([]byte, pageSize*2)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", offset, false, err
	}
	data = data[:n]
	if end := bytes.LastIndexByte(data, '\n'); end >= 0 {
		data = data[:end+1]
	} else {
		data = nil
	}
	_, failed := os.Stat(path + ".failed")
	return string(data), offset + int64(len(data)), failed == nil, nil
}

func Prune(directory string, now time.Time) error {
	if directory == "" {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > Retention {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
			_ = os.Remove(filepath.Join(directory, entry.Name()+".failed"))
			_ = os.Remove(filepath.Join(directory, entry.Name()+".done"))
		}
	}
	return nil
}
