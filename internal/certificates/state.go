package certificates

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func canonical(raw string, wildcard bool) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(raw, "."))
	if name == "" || len(name) > 253 {
		return "", errors.New("invalid DNS name")
	}
	for i, label := range strings.Split(name, ".") {
		if wildcard && i == 0 && label == "*" {
			continue
		}
		if label == "" || len(label) > 63 {
			return "", errors.New("invalid DNS label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", errors.New("invalid DNS label")
			}
		}
	}
	return name + ".", nil
}

func atomicWrite(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dispatch-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
