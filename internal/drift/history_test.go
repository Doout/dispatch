package drift

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

type historyBaselineReader map[string]core.DriftBaseline

func (r historyBaselineReader) GetDriftBaseline(_ context.Context, id string) (core.DriftBaseline, error) {
	b, ok := r[id]
	if !ok {
		return b, errors.New("unavailable")
	}
	return b, nil
}

const historyConfig = `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"config","namespace":"default","uid":"config-uid","labels":{"app":"example"}},"data":{"enabled":"true"},"spec":{"replicas":2,"items":["first","second"]}}`
const historySecret = `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credentials","namespace":"default","uid":"secret-uid"},"data":{"password":"cHJpdmF0ZQ==","token":"dG9rZW4="}}`

func historyVault(t *testing.T) *secretcrypto.Vault {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func historyEvidence(t *testing.T, vault *secretcrypto.Vault, id, raw string) core.DriftBaseline {
	t.Helper()
	ciphertext, err := vault.Encrypt(scope(id), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return core.DriftBaseline{DeploymentID: id, AppID: "app", ServerID: "server", Namespace: "default", Release: "example", Ciphertext: ciphertext}
}

func TestRecordedResourceRepeatsPrivateExactComparison(t *testing.T) {
	vault := historyVault(t)
	raw := "[" + historyConfig + "," + historySecret + "]"
	d := func(id string) core.Deployment {
		return core.Deployment{ID: id, AppID: "app", State: core.DeploymentSucceeded}
	}
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"identical", raw, true},
		{"document and key order", "[" + historySecret + "," + strings.Replace(historyConfig, `"apiVersion":"v1","kind":"ConfigMap"`, `"kind":"ConfigMap","apiVersion":"v1"`, 1) + "]", true},
		{"private secret change", strings.Replace(raw, "cHJpdmF0ZQ==", "Y2hhbmdlZA==", 1), false},
		{"private secret removal", strings.Replace(raw, `,"token":"dG9rZW4="`, "", 1), false},
		{"resource removal", "[" + historyConfig + "]", false},
		{"uid change", strings.Replace(raw, "secret-uid", "replacement-uid", 1), false},
		{"metadata change", strings.Replace(raw, `"app":"example"`, `"app":"changed"`, 1), false},
		{"array order", strings.Replace(raw, `["first","second"]`, `["second","first"]`, 1), false},
		{"ordinary value", strings.Replace(raw, `"replicas":2`, `"replicas":3`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := historyBaselineReader{"new": historyEvidence(t, vault, "new", raw), "old": historyEvidence(t, vault, "old", tc.raw)}
			before, _ := json.Marshal(reader)
			repeats := RecordedResourceRepeats(context.Background(), reader, vault, nil, []core.Deployment{d("new"), d("old")})
			if (repeats["old"] == "new") != tc.want || len(repeats) > 1 {
				t.Fatalf("unexpected links: %#v", repeats)
			}
			after, _ := json.Marshal(reader)
			if string(before) != string(after) {
				t.Fatal("comparison changed saved evidence")
			}
			output, _ := json.Marshal(repeats)
			if strings.Contains(string(output), "password") || strings.Contains(string(output), "cHJpdmF0ZQ") {
				t.Fatal("private values leaked")
			}
		})
	}
}

func TestRecordedResourceRepeatsRejectIncompleteEvidence(t *testing.T) {
	vault := historyVault(t)
	raw := "[" + historyConfig + "]"
	for _, tc := range []struct{ name, raw string }{
		{"empty", "[]"}, {"null", "null"}, {"wrong shape", historyConfig}, {"malformed", "["},
		{"trailing JSON", raw + " []"}, {"unnamed", strings.Replace(raw, `"name":"config",`, "", 1)},
		{"missing uid", strings.Replace(raw, `"uid":"config-uid",`, "", 1)},
		{"missing namespace", strings.Replace(raw, `"namespace":"default",`, "", 1)},
		{"List", strings.Replace(raw, "ConfigMap", "List", 1)},
		{"duplicate identities", "[" + historyConfig + "," + historyConfig + "]"},
		{"unsafe number", strings.Replace(raw, `"replicas":2`, `"replicas":9007199254740992`, 1)},
		{"unsafe negative number", strings.Replace(raw, `"replicas":2`, `"replicas":-9007199254740993`, 1)},
		{"unsafe exponent", strings.Replace(raw, `"replicas":2`, `"replicas":1e1000`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := historyBaselineReader{"new": historyEvidence(t, vault, "new", tc.raw), "old": historyEvidence(t, vault, "old", tc.raw)}
			items := []core.Deployment{{ID: "new", AppID: "app", State: core.DeploymentSucceeded}, {ID: "old", AppID: "app", State: core.DeploymentSucceeded}}
			if result := RecordedResourceRepeats(context.Background(), reader, vault, nil, items); len(result) != 0 {
				t.Fatal("grouped incomplete evidence", result)
			}
		})
	}
	for _, field := range []string{"id", "app", "server", "namespace", "release", "ciphertext"} {
		t.Run("baseline "+field, func(t *testing.T) {
			newer, older := historyEvidence(t, vault, "new", raw), historyEvidence(t, vault, "old", raw)
			switch field {
			case "id":
				older.DeploymentID = "new"
			case "app":
				older.AppID = "foreign"
			case "server":
				older.ServerID = "other"
			case "namespace":
				older.Namespace = ""
			case "release":
				older.Release = "different"
			case "ciphertext":
				older.Ciphertext = "corrupted"
			}
			items := []core.Deployment{{ID: "new", AppID: "app", State: core.DeploymentSucceeded}, {ID: "old", AppID: "app", State: core.DeploymentSucceeded}}
			if result := RecordedResourceRepeats(context.Background(), historyBaselineReader{"new": newer, "old": older}, vault, nil, items); len(result) != 0 {
				t.Fatal("grouped inconsistent evidence", result)
			}
		})
	}
}

func TestRecordedResourceRepeatsCursorAndBarriers(t *testing.T) {
	vault := historyVault(t)
	reader := historyBaselineReader{}
	items := []core.Deployment{}
	for _, id := range []string{"cursor", "first", "failed", "next", "missing", "older", "oldest"} {
		items = append(items, core.Deployment{ID: id, AppID: "app", State: core.DeploymentSucceeded})
		reader[id] = historyEvidence(t, vault, id, "["+historyConfig+"]")
	}
	items[2].State = core.DeploymentFailed
	delete(reader, "missing")
	result := RecordedResourceRepeats(context.Background(), reader, vault, &items[0], items[1:])
	want := map[string]string{"first": "cursor", "oldest": "older"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("got %v want %v", result, want)
	}
	if result = RecordedResourceRepeats(context.Background(), reader, nil, &items[0], items[1:]); len(result) != 0 {
		t.Fatal("grouped without vault")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result = RecordedResourceRepeats(ctx, reader, vault, nil, items); len(result) != 0 {
		t.Fatal("ignored cancellation")
	}
}
