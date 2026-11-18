package workflowfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const maxJobOutput = 64 << 10

func ReadJobOutputs(path string, declared []string) (map[string]string, error) {
	if len(declared) == 0 {
		return map[string]string{}, nil
	}
	file, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("job did not create DISPATCH_OUTPUT_FILE")
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxJobOutput+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxJobOutput {
		return nil, errors.New("job output exceeds 64 KiB")
	}
	outputs := map[string]string{}
	if json.Unmarshal(contents, &outputs) != nil {
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
			name, value, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(name) == "" {
				return nil, errors.New("job output must contain JSON or name=value lines")
			}
			outputs[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), "\"'")
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	for _, name := range declared {
		if _, ok := outputs[name]; !ok {
			return nil, fmt.Errorf("job output %s is missing", name)
		}
	}
	for name := range outputs {
		if !slices.Contains(declared, name) {
			delete(outputs, name)
		}
	}
	return outputs, nil
}
