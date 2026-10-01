// Package mock implements a provider without creating any infrastructure.
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/doout/dispatch/internal/provider"
)

type Options struct {
	StateFile  string
	Polls      int
	FailCreate bool
	FailDelete bool
}

type Mock struct {
	mu      sync.Mutex
	options Options
	state   savedState
}

type savedState struct {
	Version    int                        `json:"version"`
	Servers    map[string]provider.Server `json:"servers"`
	Operations map[string]savedOperation  `json:"operations"`
	Keys       map[string]savedKey        `json:"keys"`
}
type savedOperation struct {
	Operation provider.Operation `json:"operation"`
	Remaining int                `json:"remaining"`
	Delete    bool               `json:"delete"`
	Fail      bool               `json:"fail"`
}
type savedKey struct {
	Digest      string `json:"digest"`
	OperationID string `json:"operationId"`
}

func New(options Options) (*Mock, error) {
	if options.Polls == 0 {
		options.Polls = 2
	}
	if options.Polls < 1 || options.Polls > 1000 {
		return nil, errors.New("mock operation polls must be between 1 and 1000")
	}
	m := &Mock{options: options, state: savedState{Version: 1, Servers: map[string]provider.Server{}, Operations: map[string]savedOperation{}, Keys: map[string]savedKey{}}}
	if options.StateFile != "" {
		info, err := os.Lstat(options.StateFile)
		if err != nil && !os.IsNotExist(err) {
			return nil, errors.New("cannot inspect mock state file")
		}
		if err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 32<<20 {
				return nil, errors.New("mock state must be a private regular file within 32 MiB")
			}
			raw, err := os.ReadFile(options.StateFile)
			var loaded savedState
			if err != nil || json.Unmarshal(raw, &loaded) != nil || loaded.Version != 1 || loaded.Servers == nil || loaded.Operations == nil || loaded.Keys == nil {
				return nil, errors.New("mock state file is invalid")
			}
			m.state = loaded
			for id, op := range m.state.Operations {
				if op.Operation.ID != id || provider.ValidateOperation(op.Operation) != nil || op.Remaining < 0 || op.Remaining > 1000 {
					return nil, errors.New("mock state has an invalid operation")
				}
			}
			for _, key := range m.state.Keys {
				if _, ok := m.state.Operations[key.OperationID]; !ok {
					return nil, errors.New("mock state has an invalid operation reference")
				}
			}
		}
	}
	return m, nil
}

func (m *Mock) Manifest(context.Context) (provider.Manifest, error) {
	return provider.Manifest{
		APIVersion: provider.APIVersion, Name: "dispatch-mock", DisplayName: "Dispatch mock provider", Version: "1.0.0",
		Capabilities:        []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete},
		ConfigurationSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"testLabel":{"type":"string","maxLength":80}},"additionalProperties":false}`),
	}, nil
}

func (m *Mock) Validate(_ context.Context, config map[string]any) error {
	for key, value := range config {
		label, ok := value.(string)
		if key != "testLabel" || !ok || utf8.RuneCountInString(label) > 80 {
			return provider.NewProblem(http.StatusUnprocessableEntity, "Mock configuration invalid", "Only an optional testLabel string of at most 80 characters is supported.")
		}
	}
	return nil
}

func (m *Mock) Options(ctx context.Context, input provider.OptionRequest) ([]provider.Option, error) {
	if err := m.Validate(ctx, input.Config); err != nil {
		return nil, err
	}
	options := map[string]provider.Option{
		"regions":  {ID: "mock-region", Name: "Mock region"},
		"sizes":    {ID: "mock-small", Name: "Mock small", Metadata: map[string]any{"cpu": 1, "memoryMiB": 512}},
		"images":   {ID: "mock-linux", Name: "Mock Linux"},
		"networks": {ID: "mock-private", Name: "Mock private network"},
	}
	item, ok := options[input.Kind]
	if !ok {
		return nil, provider.NewProblem(http.StatusUnprocessableEntity, "Option kind unsupported", "Use regions, sizes, images or networks.")
	}
	return []provider.Option{item}, nil
}

func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

// Update a copy and publish it only after its state file is durable. This also
// prevents a failed disk write from leaving an accepted operation only in memory.
func (m *Mock) change(ctx context.Context, update func(*savedState) (provider.Operation, error)) (provider.Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Operation{}, err
	}
	raw, _ := json.Marshal(m.state)
	var next savedState
	_ = json.Unmarshal(raw, &next)
	operation, err := update(&next)
	if err != nil {
		return provider.Operation{}, err
	}
	if m.options.StateFile != "" {
		if err = save(m.options.StateFile, next); err != nil {
			return provider.Operation{}, errors.New("cannot persist mock operation")
		}
	}
	m.state = next
	return operation, nil
}

func save(path string, state savedState) error {
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > 32<<20 {
		return errors.New("mock state exceeds its limit")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mock-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (m *Mock) CreateServer(ctx context.Context, key string, input provider.CreateServerRequest) (provider.Operation, error) {
	if !provider.ValidID(key) {
		return provider.Operation{}, provider.NewProblem(400, "Idempotency key invalid", "Use a valid mutation key.")
	}
	raw, _ := json.Marshal(input)
	requestDigest := digest("create:" + string(raw))
	clear(raw)
	keyID := digest(key)
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		if err := m.Validate(ctx, input.ProviderConfig); err != nil {
			return provider.Operation{}, err
		}
		if strings.TrimSpace(input.Name) == "" || len(input.Name) > 80 || input.Region != "mock-region" || input.Size != "mock-small" || input.Image != "mock-linux" || input.Network != "mock-private" || input.SSHKey == "" {
			return provider.Operation{}, provider.NewProblem(422, "Mock server options invalid", "Use the advertised options, a name of at most 80 bytes and a nonempty test SSH key.")
		}
		id := "mock-server-" + keyID[:24]
		op := provider.Operation{ID: "mock-op-" + keyID[:24], State: provider.StatePending, ResourceID: id}
		labels := map[string]string{"dispatch.provider": "dispatch-mock"}
		if label, _ := input.ProviderConfig["testLabel"].(string); label != "" {
			labels["testLabel"] = label
		}
		state.Servers[id] = provider.Server{ID: id, Name: input.Name, Address: "192.0.2.10", State: "provisioning", Labels: labels}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Fail: m.options.FailCreate}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}

func conflict() error {
	return provider.NewProblem(http.StatusConflict, "Idempotency key conflict", "This key already identifies a different request. Reconcile its original operation.")
}

func (m *Mock) Operation(ctx context.Context, id string) (provider.Operation, error) {
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		saved, ok := state.Operations[id]
		if !ok {
			return provider.Operation{}, provider.NewProblem(404, "Operation not found", "The operation identity is unknown.")
		}
		if provider.Terminal(saved.Operation) {
			return saved.Operation, nil
		}
		saved.Remaining--
		saved.Operation.State = provider.StateRunning
		if saved.Remaining <= 0 {
			saved.Operation.State = provider.StateSucceeded
			server := state.Servers[saved.Operation.ResourceID]
			if saved.Fail {
				saved.Operation.State, saved.Operation.Message = provider.StateFailed, "Configured mock operation failure"
				server.State = "failed"
				state.Servers[server.ID] = server
			} else if saved.Delete {
				delete(state.Servers, saved.Operation.ResourceID)
			} else {
				server.State = "ready"
				state.Servers[server.ID] = server
			}
		}
		state.Operations[id] = saved
		return saved.Operation, nil
	})
}

func (m *Mock) Server(ctx context.Context, id string) (provider.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Server{}, err
	}
	server, ok := m.state.Servers[id]
	if !ok {
		return provider.Server{}, provider.NewProblem(404, "Server not found", "The server is absent.")
	}
	labels := map[string]string{}
	for key, value := range server.Labels {
		labels[key] = value
	}
	server.Labels = labels
	return server, nil
}

func (m *Mock) DeleteServer(ctx context.Context, key, id string) (provider.Operation, error) {
	if !provider.ValidID(key) || !provider.ValidID(id) {
		return provider.Operation{}, provider.NewProblem(400, "Deletion identity invalid", "Use valid resource and mutation identities.")
	}
	keyID, requestDigest := digest(key), digest("delete:"+id)
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		server, exists := state.Servers[id]
		if exists && (server.State == "provisioning" || server.State == "deleting") {
			return provider.Operation{}, provider.NewProblem(409, "Server operation active", "Finish the active operation before deleting this server.")
		}
		op := provider.Operation{ID: "mock-op-" + keyID[:24], State: provider.StatePending, ResourceID: id}
		if !exists {
			op.State = provider.StateSucceeded
		} else {
			server.State = "deleting"
			state.Servers[id] = server
		}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Delete: true, Fail: m.options.FailDelete}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}

// FaultHandler injects transport failures before adapter work. Failure strings
// are fixed so test input and credentials cannot become diagnostics.
func FaultHandler(next http.Handler, mode string) (http.Handler, error) {
	switch mode {
	case "", "unavailable", "malformed", "incompatible":
	default:
		return nil, errors.New("unknown mock fault mode")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "unavailable":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(provider.NewProblem(503, "Mock provider unavailable", "Configured transport failure."))
		case "malformed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"invalid":`))
		case "incompatible":
			if r.URL.Path != "/v1/manifest" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(provider.Manifest{APIVersion: "dispatch.provider/v999", Name: "dispatch-mock", DisplayName: "Mock incompatible provider", Version: "999.0.0"})
		default:
			next.ServeHTTP(w, r)
		}
	}), nil
}
