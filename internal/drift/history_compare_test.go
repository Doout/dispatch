package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

func comparisonDeployment(id string) core.Deployment {
	return core.Deployment{ID: id, AppID: "app", State: core.DeploymentSucceeded, Snapshot: core.DeploymentSnapshot{TargetID: "server", Namespace: "default", Release: "example"}}
}

func compareEvidence(t *testing.T, left, right string) RecordedComparison {
	t.Helper()
	vault := historyVault(t)
	reader := historyBaselineReader{"old": historyEvidence(t, vault, "old", left), "new": historyEvidence(t, vault, "new", right)}
	return CompareRecordedResources(context.Background(), reader, vault, comparisonDeployment("old"), comparisonDeployment("new"))
}

const comparedDeployment = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default"},"spec":{"replicas":2,"revisionHistoryLimit":3,"template":{"metadata":{"annotations":{"checksum/config":"old-checksum"}},"spec":{"containers":[{"name":"api","image":"example/api:v1","env":[{"name":"DATABASE_PASSWORD","value":"old-credential"}]}]}}}}`

func TestRecordedComparisonUsesHelmRenderedResources(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "example", Version: "1.0.0"}, Values: map[string]any{"replicas": 2, "revisionHistoryLimit": 3}, Templates: []*chart.File{{Name: "templates/deployment.yaml", Data: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: default
spec:
  replicas: {{ .Values.replicas }}
  revisionHistoryLimit: {{ .Values.revisionHistoryLimit }}
  template:
    spec:
      containers:
        - name: api
          image: example/api:{{ .Values.images.api.tag }}
`)}}}
	render := func(tag, digest string, replicas int) string {
		t.Helper()
		values := map[string]any{"images": map[string]any{"api": map[string]any{"tag": tag, "digest_amd64": digest}}, "replicas": replicas}
		renderValues, err := chartutil.ToRenderValues(c, values, chartutil.ReleaseOptions{Name: "example", Namespace: "default", Revision: 1, IsInstall: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		files, err := engine.Render(c, renderValues)
		if err != nil {
			t.Fatal(err)
		}
		objects, err := Parse(files["example/templates/deployment.yaml"], "default")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(objects)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	original := render("v1", "unused-before", 2)
	if result := compareEvidence(t, original, render("v1", "unused-after", 2)); !result.Available || len(result.Changes) != 0 {
		t.Fatal("unused input changed rendered comparison", result)
	}
	c.Values["revisionHistoryLimit"] = 4
	result := compareEvidence(t, original, render("v2", "another-unused", 5))
	if !result.Available || len(result.Changes) != 3 || result.Hidden != 0 {
		t.Fatal("expected image, replicas, and chart default changes", result)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), "example/api:v2") || strings.Contains(string(encoded), "digest_amd64") || strings.Contains(string(encoded), "/values/") {
		t.Fatal(string(encoded))
	}
}

func TestRecordedComparisonIgnoresInputsAndProvenance(t *testing.T) {
	vault := historyVault(t)
	before := strings.Replace(comparedDeployment, `"namespace":"default"`, `"namespace":"default","uid":"old-uid","resourceVersion":"17","generation":2,"annotations":{"dispatch.app/provenance":"old-provenance","deployment.kubernetes.io/revision":"7"},"labels":{"dispatch.app/deployment-id":"old"}`, 1)
	after := strings.Replace(comparedDeployment, `"namespace":"default"`, `"namespace":"default","uid":"new-uid","resourceVersion":"25","generation":3,"annotations":{"dispatch.app/provenance":"new-provenance","deployment.kubernetes.io/revision":"8"},"labels":{"dispatch.app/deployment-id":"new"}`, 1)
	reader := historyBaselineReader{"old": historyEvidence(t, vault, "old", "["+before+"]"), "new": historyEvidence(t, vault, "new", "["+after+"]")}
	from, to := comparisonDeployment("old"), comparisonDeployment("new")
	from.CommitSHA, to.CommitSHA = "old-revision", "new-revision"
	from.Snapshot.Values, to.Snapshot.Values = map[string]any{"unused": "before"}, map[string]any{"unused": "after"}
	result := CompareRecordedResources(context.Background(), reader, vault, from, to)
	if !result.Available || len(result.Changes) != 0 {
		t.Fatal("bookkeeping or unused inputs changed comparison", result)
	}
	if result := compareEvidence(t, "["+comparedDeployment+"]", "["+strings.Replace(comparedDeployment, "old-checksum", "new-checksum", 1)+"]"); !result.Available || len(result.Changes) != 1 || result.Hidden != 1 || !strings.HasSuffix(result.Changes[0].Path, "/template/metadata/annotations") {
		t.Fatal("lost pod template annotation change", result)
	}
}

func TestRecordedComparisonRedactsPrivateSections(t *testing.T) {
	secret := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credentials","namespace":"default"},"data":{"secret-key-before":"secret-value-before"}}`
	config := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"config","namespace":"default"},"data":{"private-key-before":"private-value-before"}}`
	custom := `{"apiVersion":"custom.example/v1","kind":"Account","metadata":{"name":"account","namespace":"default"},"spec":{"private-before":{"image":"private-image-before","pin":12345}}}`
	before := "[" + comparedDeployment + "," + secret + "," + config + "," + custom + "]"
	after := strings.NewReplacer("old-credential", "new-credential", "-before", "-after", "12345", "67890").Replace(before)
	result := compareEvidence(t, before, after)
	if !result.Available || len(result.Changes) != 4 || result.Hidden != 4 {
		t.Fatal("expected four private sections", result)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"old-credential", "new-credential", "secret-key", "secret-value", "private-key", "private-value", "private-image", "private-before", "private-after", "12345", "67890"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leaked %s in %s", forbidden, encoded)
		}
	}
	for _, change := range result.Changes {
		if change.Before != recordedRedacted || change.After != recordedRedacted {
			t.Fatal("returned a private value", change)
		}
	}
}

func TestRecordedComparisonAddsRemovesAndReordersResources(t *testing.T) {
	secret := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credentials","namespace":"default"},"data":{"password":"must-never-leak"}}`
	full := "[" + comparedDeployment + "," + secret + "]"
	if result := compareEvidence(t, full, "["+secret+","+comparedDeployment+"]"); !result.Available || len(result.Changes) != 0 {
		t.Fatal("document ordering changed comparison", result)
	}
	for _, tc := range []struct{ before, after, kind string }{{"[" + comparedDeployment + "]", full, "added"}, {full, "[" + comparedDeployment + "]", "removed"}, {"[]", "[" + secret + "]", "added"}} {
		result := compareEvidence(t, tc.before, tc.after)
		if !result.Available || len(result.Changes) != 1 || result.Changes[0].Kind != tc.kind {
			t.Fatal(result)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "must-never-leak") || !strings.Contains(string(encoded), "Present") {
			t.Fatal(string(encoded))
		}
	}
	result := compareEvidence(t, "["+comparedDeployment+"]", "["+strings.Replace(comparedDeployment, `"revisionHistoryLimit":3,`, "", 1)+"]")
	if len(result.Changes) != 1 || result.Changes[0].Kind != "removed" || result.Changes[0].Before != json.Number("3") || result.Changes[0].After != nil {
		t.Fatal("removed field lost", result)
	}
}

func TestRecordedComparisonRequiresCompleteEvidence(t *testing.T) {
	vault := historyVault(t)
	valid := "[" + comparedDeployment + "]"
	many := []string{}
	for i := 0; i < 501; i++ {
		many = append(many, strings.Replace(comparedDeployment, `"name":"api"`, fmt.Sprintf(`"name":"api-%d"`, i), 1))
	}
	for _, tc := range []struct{ name, raw string }{
		{"null", "null"}, {"wrong shape", comparedDeployment}, {"malformed", "["}, {"trailing JSON", valid + " []"},
		{"null object", "[null]"}, {"unnamed", strings.Replace(valid, `"name":"api",`, "", 1)},
		{"duplicate resources", "[" + comparedDeployment + "," + comparedDeployment + "]"},
		{"wrong namespace type", strings.Replace(valid, `"namespace":"default"`, `"namespace":42`, 1)},
		{"wrong labels type", strings.Replace(valid, `"namespace":"default"`, `"namespace":"default","labels":[]`, 1)},
		{"wrong annotation value", strings.Replace(valid, `"namespace":"default"`, `"namespace":"default","annotations":{"key":42}`, 1)},
		{"unsafe number", strings.Replace(valid, `"replicas":2`, `"replicas":9007199254740993`, 1)},
		{"too many resources", "[" + strings.Join(many, ",") + "]"},
		{"deep data", strings.Replace(valid, `"replicas":2`, `"replicas":`+strings.Repeat("[", 70)+"0"+strings.Repeat("]", 70), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := historyBaselineReader{"old": historyEvidence(t, vault, "old", valid), "new": historyEvidence(t, vault, "new", tc.raw)}
			result := CompareRecordedResources(context.Background(), reader, vault, comparisonDeployment("old"), comparisonDeployment("new"))
			if result.Available || len(result.Changes) != 0 {
				t.Fatal("accepted invalid saved resources", result)
			}
		})
	}
	for _, field := range []string{"id", "app", "target", "namespace", "release", "ciphertext", "large ciphertext", "missing", "state", "snapshot", "nil vault", "cancelled"} {
		t.Run(field, func(t *testing.T) {
			from, to := comparisonDeployment("old"), comparisonDeployment("new")
			before, after := historyEvidence(t, vault, "old", valid), historyEvidence(t, vault, "new", valid)
			reader := historyBaselineReader{"old": before, "new": after}
			ctx := context.Background()
			var key *secretcrypto.Vault = vault
			switch field {
			case "id":
				after.DeploymentID = "old"
			case "app":
				after.AppID = "other"
			case "target":
				after.ServerID = "other"
			case "namespace":
				after.Namespace = "other"
			case "release":
				after.Release = "other"
			case "ciphertext":
				after.Ciphertext = "corrupted"
			case "large ciphertext":
				after.Ciphertext = strings.Repeat("x", 24<<20+1)
			case "missing":
				delete(reader, "old")
			case "state":
				to.State = core.DeploymentFailed
			case "snapshot":
				to.Snapshot.TargetID = ""
			case "nil vault":
				key = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			reader["new"] = after
			result := CompareRecordedResources(ctx, reader, key, from, to)
			if result.Available || len(result.Changes) != 0 {
				t.Fatal("accepted inconsistent evidence", result)
			}
		})
	}
}

func TestRecordedComparisonDifferentAuthorizedAppsAndLocations(t *testing.T) {
	vault := historyVault(t)
	from, to := comparisonDeployment("old"), comparisonDeployment("new")
	to.AppID, to.Snapshot.TargetID, to.Snapshot.Release = "other-app", "other-target", "other-release"
	before, after := historyEvidence(t, vault, "old", "[]"), historyEvidence(t, vault, "new", "[]")
	after.AppID, after.ServerID, after.Release = to.AppID, to.Snapshot.TargetID, to.Snapshot.Release
	reader := historyBaselineReader{"old": before, "new": after}
	saved, _ := json.Marshal(reader)
	result := CompareRecordedResources(context.Background(), reader, vault, from, to)
	if !result.Available || len(result.Changes) != 2 || result.Changes[0].Path != "/release/target" || result.Changes[1].Path != "/release/name" {
		t.Fatal("lost saved release location changes", result)
	}
	unchanged, _ := json.Marshal(reader)
	if !reflect.DeepEqual(saved, unchanged) {
		t.Fatal("mutated stored evidence")
	}
}

func TestRecordedComparisonTruncatesAndKeepsStableOrder(t *testing.T) {
	before, after := []string{}, []string{}
	for i := 0; i < 400; i++ {
		obj := strings.Replace(comparedDeployment, `"name":"api"`, fmt.Sprintf(`"name":"api-%03d"`, i), 1)
		before = append(before, obj)
		after = append(after, strings.NewReplacer(`"replicas":2`, `"replicas":3`, `"revisionHistoryLimit":3`, `"revisionHistoryLimit":4`, "example/api:v1", "example/api:v2").Replace(obj))
	}
	result := compareEvidence(t, "["+strings.Join(before, ",")+"]", "["+strings.Join(after, ",")+"]")
	if !result.Available || !result.Truncated || len(result.Changes) != 1000 {
		t.Fatal("expected bounded comparison", result)
	}
	for i := 1; i < len(result.Changes); i++ {
		if result.Changes[i-1].Path >= result.Changes[i].Path {
			t.Fatal("unstable path order")
		}
	}
}

func TestRecordedComparisonRetainsEmptySectionsAndHidesMapKeys(t *testing.T) {
	for _, field := range []struct{ before, after string }{
		{`"image":"example/api:v1"`, `"image":"example/api:v1","resources":{}`},
		{`"containers":[`, `"initContainers":[],"containers":[`},
	} {
		before := "[" + comparedDeployment + "]"
		after := strings.Replace(before, field.before, field.after, 1)
		for _, pair := range [][2]string{{before, after}, {after, before}} {
			result := compareEvidence(t, pair[0], pair[1])
			if !result.Available || len(result.Changes) != 1 || result.Hidden != 1 {
				t.Fatal("empty section presence change lost", result)
			}
		}
	}
	before := strings.Replace("["+comparedDeployment+"]", `"replicas":2,`, `"replicas":2,"nodeSelector":{"private-label-before":"one"},"volumes":[{"csi":{"volumeAttributes":{"private-parameter-before":"two"}}}],`, 1)
	after := strings.ReplaceAll(before, "-before", "-after")
	result := compareEvidence(t, before, after)
	if !result.Available || len(result.Changes) != 1 || result.Hidden != 1 {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private-label") || strings.Contains(string(raw), "private-parameter") {
		t.Fatal("private map key in path", string(raw))
	}
}
