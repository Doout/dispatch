package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOverviewDiffFieldsAndRecordOrder(t *testing.T) {
	var before, after any
	json.Unmarshal([]byte(`{"apps":[{"id":"a","state":"queued","document":"large unchanged document"},{"id":"b","state":"ready"}],"meta":{"a/b~c":true,"gone":1}}`), &before)
	json.Unmarshal([]byte(`{"apps":[{"id":"b","state":"ready"},{"id":"a","state":"ready","document":"large unchanged document"},{"id":"c","state":"queued"}],"meta":{"a/b~c":false,"zero":0,"nil":null}}`), &after)
	var ops []overviewPatch
	diffOverview(before, after, "", &ops)
	data, _ := json.Marshal(ops)
	for _, want := range []string{`"op":"move"`, `"path":"/apps/1/state","value":"ready"`, `"path":"/apps/2"`, `"path":"/meta/a~1b~0c","value":false`, `"value":0`, `"value":null`, `"op":"remove"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), "large unchanged document") {
		t.Fatalf("resent unchanged data: %s", data)
	}
	ops = nil
	diffOverview(after, after, "", &ops)
	if len(ops) != 0 {
		t.Fatal("idle update")
	}
}
func TestOverviewBaselineScopeAndEviction(t *testing.T) {
	var cache overviewCache
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer owner")
	scope := overviewScope(req)
	version := cache.remember(scope, []byte(`{}`))
	req.Header.Set("Impersonate-User", "member")
	if _, ok := cache.find(overviewScope(req), version); ok {
		t.Fatal("baseline crossed identity boundary")
	}
	for i := 0; i < 65; i++ {
		data, _ := json.Marshal(i)
		cache.remember(scope, data)
	}
	if _, ok := cache.find(scope, version); ok {
		t.Fatal("old baseline not evicted")
	}
}

func TestOverviewDeletionDoesNotMoveUnchangedRecords(t *testing.T) {
	before := []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}, map[string]any{"id": "c"}}
	after := before[1:]
	var ops []overviewPatch
	diffOverview(before, after, "/apps", &ops)
	if len(ops) != 1 || ops[0].Op != "remove" || ops[0].Path != "/apps/0" {
		t.Fatalf("nonminimal deletion: %#v", ops)
	}
}
