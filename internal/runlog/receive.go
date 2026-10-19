package runlog

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/logsafe"
)

// Receive accepts complete, filtered lines at an exact byte cursor. Repeating
// a request after response loss verifies the saved prefix instead of appending
// it again. Gaps, changed prefixes and unfiltered chunks are rejected.
func Receive(directory, id string, offset int64, text string, secrets ...string) error {
	if directory == "" || !idPattern.MatchString(id) || offset < 0 || len(text) > pageSize*2 || offset+int64(len(text)) > MaxBytes || !strings.HasSuffix(text, "\n") || logsafe.Text(text, secrets...) != text {
		return errors.New("invalid filtered log chunk")
	}
	quota.Lock()
	defer quota.Unlock()
	if err := prepareDirectory(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, id+".log")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < offset {
		return errors.New("log cursor does not match the saved prefix")
	}
	saved := make([]byte, min(info.Size()-offset, int64(len(text))))
	if len(saved) > 0 {
		if _, err := file.ReadAt(saved, offset); err != nil {
			return err
		}
		if !bytes.Equal(saved, []byte(text)[:len(saved)]) {
			return errors.New("log prefix changed")
		}
	}
	if len(saved) == len(text) {
		return nil
	}
	if err := checkQuota(directory, int64(len(text)-len(saved))); err != nil {
		return err
	}
	if _, err := io.WriteString(io.NewOffsetWriter(file, offset+int64(len(saved))), text[len(saved):]); err != nil {
		return err
	}
	return file.Sync()
}

func Finish(directory, id string, size int64) error {
	if directory == "" || !idPattern.MatchString(id) || size < 0 || size > MaxBytes {
		return errors.New("invalid log receipt")
	}
	quota.Lock()
	defer quota.Unlock()
	path := filepath.Join(directory, id+".log")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != size {
		return errors.New("log transfer is incomplete")
	}
	return os.WriteFile(path+".done", nil, 0600)
}

func Position(directory, id string) int64 {
	if directory == "" || !idPattern.MatchString(id) {
		return 0
	}
	info, err := os.Stat(filepath.Join(directory, id+".log"))
	if err != nil {
		return 0
	}
	return info.Size()
}
