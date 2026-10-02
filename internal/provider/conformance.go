package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

type ConformanceOptions struct {
	Request      CreateServerRequest
	PollInterval time.Duration
	Timeout      time.Duration
}
type ConformanceCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}
type ConformanceReport struct {
	APIVersion string             `json:"apiVersion"`
	Checks     []ConformanceCheck `json:"checks"`
}

// RunConformance creates a disposable server and removes it before returning.
// Run it only against an adapter/account dedicated to conformance testing.
func RunConformance(ctx context.Context, adapter Provider, options ConformanceOptions) (report ConformanceReport, resultErr error) {
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	report = ConformanceReport{APIVersion: APIVersion, Checks: []ConformanceCheck{}}
	check := func(name string, run func() error) bool {
		err := run()
		item := ConformanceCheck{Name: name, Passed: err == nil}
		if err != nil {
			item.Message = err.Error()
			resultErr = fmt.Errorf("provider conformance failed: %s", name)
		}
		report.Checks = append(report.Checks, item)
		return err == nil
	}
	if !check("manifest and configuration schema", func() error {
		manifest, err := adapter.Manifest(ctx)
		if err != nil {
			return errors.New("manifest request failed or returned invalid data")
		}
		if err = ValidateManifest(manifest); err != nil {
			return err
		}
		for _, capability := range []string{CapabilityCreate, CapabilityInspect, CapabilityDelete} {
			if !slices.Contains(manifest.Capabilities, capability) {
				return errors.New("the server lifecycle capability set is incomplete")
			}
		}
		return nil
	}) {
		return
	}
	if !check("configuration validation", func() error {
		if adapter.Validate(ctx, options.Request.ProviderConfig) != nil {
			return errors.New("the supplied configuration was not accepted")
		}
		return nil
	}) {
		return
	}
	if !check("advertised server options", func() error {
		for kind, selected := range map[string]string{"regions": options.Request.Region, "sizes": options.Request.Size, "images": options.Request.Image, "networks": options.Request.Network} {
			items, err := adapter.Options(ctx, OptionRequest{Kind: kind, Config: options.Request.ProviderConfig})
			if err != nil || items == nil {
				return errors.New("option discovery failed")
			}
			found := selected == ""
			seen := map[string]bool{}
			for _, item := range items {
				if item.ID == "" || item.Name == "" || seen[item.ID] {
					return errors.New("options contain missing identities, names or duplicate entries")
				}
				seen[item.ID] = true
				found = found || item.ID == selected
			}
			if !found {
				return fmt.Errorf("the supplied %s selection is not advertised", kind)
			}
		}
		return nil
	}) {
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return report, errors.New("cannot generate conformance request identity")
	}
	prefix := "conformance-" + hex.EncodeToString(random)
	request := options.Request
	request.Name = prefix
	var create Operation
	cleaned := false
	defer func() {
		if cleaned || create.ID == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.Timeout)
		defer cancel()
		// Inspection can complete an uncertain create before deletion is safe.
		observed, err := waitOperation(cleanupCtx, adapter, create, options.PollInterval)
		if err != nil || observed.ResourceID == "" {
			report.Checks = append(report.Checks, ConformanceCheck{Name: "failure cleanup", Message: "create outcome is uncertain; inspect the test adapter for retained resources"})
			if resultErr == nil {
				resultErr = errors.New("conformance cleanup needs operator inspection")
			}
			return
		}
		deletion, err := adapter.DeleteServer(cleanupCtx, prefix+"-delete", observed.ResourceID)
		if err == nil {
			deletion, err = waitOperation(cleanupCtx, adapter, deletion, options.PollInterval)
		}
		ok := err == nil && deletion.State == StateSucceeded
		item := ConformanceCheck{Name: "failure cleanup", Passed: ok}
		if !ok {
			item.Message = "test resource cleanup failed; inspect the adapter before another run"
			if resultErr == nil {
				resultErr = errors.New(item.Message)
			}
		}
		report.Checks = append(report.Checks, item)
	}()
	if !check("asynchronous create", func() error {
		var err error
		create, err = adapter.CreateServer(ctx, prefix+"-create", request)
		if err != nil {
			return errors.New("server creation did not return an accepted operation")
		}
		return ValidateOperation(create)
	}) {
		return
	}
	if !check("concurrent create idempotency", func() error {
		var wg sync.WaitGroup
		failures := make(chan bool, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				again, err := adapter.CreateServer(ctx, prefix+"-create", request)
				failures <- err != nil || again.ID != create.ID || create.ResourceID != "" && again.ResourceID != create.ResourceID
			}()
		}
		wg.Wait()
		close(failures)
		for failed := range failures {
			if failed {
				return errors.New("repeated create requests returned different operation/resource identities or failed")
			}
		}
		return nil
	}) {
		return
	}
	if !check("idempotency payload conflict", func() error {
		changed := request
		changed.Name = "changed-" + hex.EncodeToString(random)
		_, err := adapter.CreateServer(ctx, prefix+"-create", changed)
		var problem *Problem
		if !errors.As(err, &problem) || problem.Status != http.StatusConflict {
			return errors.New("reusing a mutation key for different inputs did not return a 409 problem")
		}
		return nil
	}) {
		return
	}
	if !check("create state transitions", func() error {
		var err error
		create, err = waitOperation(ctx, adapter, create, options.PollInterval)
		if err != nil {
			return err
		}
		if create.State != StateSucceeded {
			return errors.New("creation reached an unsuccessful terminal state")
		}
		return nil
	}) {
		return
	}
	if !check("resource inspection and stable terminal operation", func() error {
		server, err := adapter.Server(ctx, create.ResourceID)
		if err != nil || server.ID != create.ResourceID || server.Name != request.Name || server.State != "ready" || server.Address == "" {
			return errors.New("completed creation did not expose the expected ready server")
		}
		again, err := adapter.Operation(ctx, create.ID)
		if err != nil || again.ID != create.ID || again.State != create.State || again.ResourceID != create.ResourceID {
			return errors.New("a terminal operation changed after completion")
		}
		return nil
	}) {
		return
	}
	if !check("asynchronous delete and idempotency", func() error {
		deletion, err := adapter.DeleteServer(ctx, prefix+"-delete", create.ResourceID)
		if err != nil {
			return errors.New("server deletion was not accepted")
		}
		again, err := adapter.DeleteServer(ctx, prefix+"-delete", create.ResourceID)
		if err != nil || deletion.ID != again.ID || deletion.ResourceID != again.ResourceID {
			return errors.New("repeated deletion did not preserve operation and resource identities")
		}
		deletion, err = waitOperation(ctx, adapter, deletion, options.PollInterval)
		if err != nil || deletion.State != StateSucceeded {
			return errors.New("server deletion did not complete successfully")
		}
		return nil
	}) {
		return
	}
	if !check("absent server and repeated deletion", func() error {
		_, err := adapter.Server(ctx, create.ResourceID)
		var problem *Problem
		if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
			return errors.New("a deleted resource did not return a 404 problem")
		}
		again, err := adapter.DeleteServer(ctx, prefix+"-delete", create.ResourceID)
		if err != nil || again.State != StateSucceeded {
			return errors.New("a completed delete could not be replayed")
		}
		absent, err := adapter.DeleteServer(ctx, prefix+"-absent", create.ResourceID)
		if err == nil {
			absent, err = waitOperation(ctx, adapter, absent, options.PollInterval)
		}
		if err != nil || absent.State != StateSucceeded {
			return errors.New("deleting an already absent resource was not successful")
		}
		cleaned = true
		return nil
	}) {
		return
	}
	return report, nil
}

func waitOperation(ctx context.Context, adapter Provider, initial Operation, interval time.Duration) (Operation, error) {
	current := initial
	for {
		if err := ValidateOperation(current); err != nil {
			return current, err
		}
		if Terminal(current) {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(interval):
		}
		next, err := adapter.Operation(ctx, current.ID)
		if err != nil {
			return current, errors.New("operation polling failed")
		}
		if next.ID != initial.ID || current.ResourceID != "" && next.ResourceID != current.ResourceID || current.State == StateRunning && next.State == StatePending {
			return current, errors.New("operation identity changed or its state moved backwards")
		}
		current = next
	}
}
