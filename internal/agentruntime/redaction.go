package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
)

// Keep an encrypted redaction dictionary across credential rotations so later
// log requests cannot expose credentials emitted by an earlier deployment.
func (w *Worker) redactor(ctx context.Context, request remoteruntime.Request) (func(string) string, error) {
	scope := "redaction:" + request.Application.ID
	name := "redaction-" + request.Application.ID
	record := struct {
		Complete bool            `json:"complete"`
		Secrets  map[string]bool `json:"secrets"`
	}{Secrets: map[string]bool{}}
	encrypted, err := os.ReadFile(filepath.Join(w.directory, name))
	if err == nil {
		raw, e := w.vault.Decrypt(scope, string(encrypted))
		if e != nil {
			return nil, e
		}
		err = json.Unmarshal(raw, &record)
		clear(raw)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		if request.Operation == runtimecontract.Deploy {
			existing, checkErr := w.hasWorkload(ctx, request)
			if checkErr != nil {
				return nil, checkErr
			}
			record.Complete = !existing
		}
	}
	if request.Operation == runtimecontract.Logs && !record.Complete {
		return nil, errors.New("runtime logs require complete retained credential redaction history")
	}
	known := record.Secrets
	if known == nil {
		return nil, errors.New("runtime redaction record is invalid")
	}
	for _, value := range request.SecretValues() {
		known[value] = true
	}
	delete(known, "")
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if len(raw) > 1<<20 {
		return nil, errors.New("retained secret redaction dictionary exceeds its limit")
	}
	ciphertext, err := w.vault.Encrypt(scope, raw)
	if err != nil {
		return nil, err
	}
	if err = w.save(name, []byte(ciphertext)); err != nil {
		return nil, err
	}
	secrets := make([]string, 0, len(known))
	for value := range known {
		secrets = append(secrets, value)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return func(value string) string {
		for _, secret := range secrets {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
		return value
	}, nil
}
