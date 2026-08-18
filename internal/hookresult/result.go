// Package hookresult defines the versioned contract shared by deployment
// hooks, the hook helper CLI, and the deployment executor.
package hookresult

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
)

const (
	CurrentVersion = 1
	MaxFileBytes   = 64 * 1024
	MaxValueBytes  = 16 * 1024
)

var (
	outputKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	helmPathPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
	helmKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type Result struct {
	Version    int               `json:"version"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Deployment *Deployment       `json:"deployment,omitempty"`
}

type Deployment struct {
	HelmValues map[string]any `json:"helmValues,omitempty"`
}

func Empty() Result {
	return Result{Version: CurrentVersion, Outputs: map[string]string{}}
}

func Read(path string) (Result, error) {
	if strings.TrimSpace(path) == "" {
		return Result{}, errors.New("hook result path is required")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Result{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Result{}, errors.New("hook result must be a regular file")
	}
	if info.Size() > MaxFileBytes {
		return Result{}, fmt.Errorf("hook result exceeds %d KiB", MaxFileBytes/1024)
	}
	contents, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Result{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("hook result must be valid versioned JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Result{}, errors.New("hook result contains more than one JSON value")
	}
	if err := Validate(result); err != nil {
		return Result{}, err
	}
	if result.Outputs == nil {
		result.Outputs = map[string]string{}
	}
	return result, nil
}

func Validate(result Result) error {
	if result.Version != CurrentVersion {
		return fmt.Errorf("unsupported hook result version %d", result.Version)
	}
	for key, value := range result.Outputs {
		if err := ValidateOutput(key, value); err != nil {
			return err
		}
	}
	if result.Deployment != nil {
		if err := validateHelmValues(result.Deployment.HelmValues); err != nil {
			return err
		}
	}
	return nil
}

func ValidateOutput(key, value string) error {
	if !outputKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid output key %q", key)
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("output %q exceeds %d KiB", key, MaxValueBytes/1024)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("output %q contains a null byte", key)
	}
	if key == "namespace" || key == "release" {
		return fmt.Errorf("output %q is immutable", key)
	}
	return nil
}

func SetOutput(path, key, value string) error {
	if err := ValidateOutput(key, value); err != nil {
		return err
	}
	return update(path, func(result *Result) error {
		if result.Outputs == nil {
			result.Outputs = map[string]string{}
		}
		result.Outputs[key] = value
		return nil
	})
}

func SetHelmValue(path, valuePath, value string) error {
	if !helmPathPattern.MatchString(valuePath) {
		return fmt.Errorf("invalid Helm value path %q", valuePath)
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("Helm value %q exceeds %d KiB", valuePath, MaxValueBytes/1024)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("Helm value %q contains a null byte", valuePath)
	}
	return update(path, func(result *Result) error {
		if result.Deployment == nil {
			result.Deployment = &Deployment{}
		}
		if result.Deployment.HelmValues == nil {
			result.Deployment.HelmValues = map[string]any{}
		}
		setValue(result.Deployment.HelmValues, valuePath, value)
		return nil
	})
}

func OutputEnvironment(outputs map[string]string) ([]string, error) {
	environment := make([]string, 0, len(outputs))
	seen := map[string]string{}
	for key, value := range outputs {
		if err := ValidateOutput(key, value); err != nil {
			return nil, err
		}
		name := "DISPATCH_OUTPUT_" + environmentName(key)
		if previous, exists := seen[name]; exists && previous != key {
			return nil, fmt.Errorf("output keys %q and %q normalize to the same environment variable", previous, key)
		}
		seen[name] = key
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func update(path string, mutate func(*Result) error) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("DISPATCH_RESULT_FILE is not set")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	result, err := Read(path)
	if err != nil {
		return err
	}
	if err := mutate(&result); err != nil {
		return err
	}
	if err := Validate(result); err != nil {
		return err
	}
	contents, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(contents) > MaxFileBytes {
		return fmt.Errorf("hook result exceeds %d KiB", MaxFileBytes/1024)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dispatch-result-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func validateHelmValues(values map[string]any) error {
	for key, value := range values {
		if !helmKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid Helm value key %q", key)
		}
		switch typed := value.(type) {
		case string:
			if len(typed) > MaxValueBytes {
				return fmt.Errorf("Helm value %q exceeds %d KiB", key, MaxValueBytes/1024)
			}
			if strings.ContainsRune(typed, 0) {
				return fmt.Errorf("Helm value %q contains a null byte", key)
			}
		case map[string]any:
			if err := validateHelmValues(typed); err != nil {
				return err
			}
		default:
			return fmt.Errorf("Helm value %q must be a string or object", key)
		}
	}
	return nil
}

func setValue(root map[string]any, path, value string) {
	parts := strings.Split(path, ".")
	cursor := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	cursor[parts[len(parts)-1]] = value
}

func environmentName(value string) string {
	var builder strings.Builder
	var previous rune
	for index, current := range value {
		if unicode.IsUpper(current) && index > 0 && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			builder.WriteByte('_')
		}
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			builder.WriteRune(unicode.ToUpper(current))
		} else if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "_") {
			builder.WriteByte('_')
		}
		previous = current
	}
	return strings.Trim(builder.String(), "_")
}
