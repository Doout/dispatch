package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testZoneID = "0123456789abcdef0123456789abcdef"
const testToken = "private-cloudflare-test-token"

type fakeCloudflare struct {
	mu                                sync.Mutex
	t                                 *testing.T
	zone                              string
	entries                           map[string]cfRecord
	next, mutations, failAt, pageSize int
	hook                              func(http.ResponseWriter, *http.Request) bool
}

func fixture(t *testing.T) (*Cloudflare, *fakeCloudflare) {
	t.Helper()
	f := &fakeCloudflare{t: t, zone: "example.test", entries: map[string]cfRecord{}, pageSize: 2}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := NewCloudflare(CloudflareConfig{APIToken: testToken, ZoneID: testZoneID, RootDomain: "dispatch.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	c.endpoint = server.URL
	t.Cleanup(c.client.CloseIdleConnections)
	return c, f
}

func (f *fakeCloudflare) add(record cfRecord) string {
	f.next++
	record.ID = fmt.Sprintf("%032x", f.next)
	f.entries[record.ID] = record
	return record.ID
}

func (f *fakeCloudflare) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Error("missing authorization")
		http.Error(w, "bad token", 401)
		return
	}
	if f.hook != nil && f.hook(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		f.mutations++
		if f.mutations == f.failAt {
			http.Error(w, testToken+" internal provider error", 503)
			return
		}
	}
	write := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result}) }
	base := "/zones/" + testZoneID
	if r.URL.Path == base && r.Method == http.MethodGet {
		write(map[string]any{"id": testZoneID, "name": f.zone, "name_servers": []string{"a.ns.cloudflare.com", "b.ns.cloudflare.com"}})
		return
	}
	if r.URL.Path == base+"/dns_records" && r.Method == http.MethodGet {
		if r.URL.Query().Get("type") != "" || r.URL.Query().Get("name.exact") == "" {
			f.t.Error("record lookup must include other types at the same exact name")
		}
		entries := []cfRecord{}
		for _, entry := range f.entries {
			if entry.Name == r.URL.Query().Get("name.exact") {
				entries = append(entries, entry)
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages := (len(entries) + f.pageSize - 1) / f.pageSize
		start := min((page-1)*f.pageSize, len(entries))
		end := min(start+f.pageSize, len(entries))
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": entries[start:end], "result_info": map[string]int{"page": page, "total_pages": pages, "total_count": len(entries)}})
		return
	}
	if r.URL.Path == base+"/dns_records" && r.Method == http.MethodPost {
		var entry cfRecord
		if json.NewDecoder(r.Body).Decode(&entry) != nil {
			f.t.Error("invalid creation payload")
		}
		id := f.add(entry)
		write(f.entries[id])
		return
	}
	id, found := strings.CutPrefix(r.URL.Path, base+"/dns_records/")
	entry, exists := f.entries[id]
	if !found || !exists {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		write(entry)
	case http.MethodPatch:
		var next cfRecord
		if json.NewDecoder(r.Body).Decode(&next) != nil {
			f.t.Error("invalid update payload")
		}
		next.ID = id
		f.entries[id] = next
		write(next)
	case http.MethodDelete:
		delete(f.entries, id)
		write(map[string]string{"id": id})
	default:
		f.t.Error("unexpected API method", r.Method)
		http.Error(w, "unexpected", 405)
	}
}

func testRecord() Record {
	return Record{ID: "tenant-address", Name: "alpha.dispatch.example.test", Type: "A", Values: []string{"192.0.2.10"}, TTL: 300}
}

func TestCloudflareCreateUpdateDeleteAndReplay(t *testing.T) {
	c, f := fixture(t)
	r := testRecord()
	r.Values = append(r.Values, "192.0.2.11")
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 2 || f.mutations != 2 {
		t.Fatal("records not created", f.entries, f.mutations)
	}
	for _, entry := range f.entries {
		if entry.Comment != "dispatch:"+r.ID || entry.Proxied {
			t.Fatal("record ownership or proxy mode", entry)
		}
	}
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if f.mutations != 2 {
		t.Fatal("replay changed already correct records")
	}
	r.Values, r.TTL = []string{"192.0.2.12"}, 600
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 1 || f.mutations != 4 {
		t.Fatal("update did not converge", f.entries)
	}
	for _, entry := range f.entries {
		if entry.Content != "192.0.2.12" || entry.TTL != 600 {
			t.Fatal(entry)
		}
	}
	if err := c.Delete(t.Context(), Record{ID: r.ID, Name: r.Name, Type: r.Type}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 0 || f.mutations != 5 {
		t.Fatal("delete replay mutated records", f.entries)
	}
}

func TestCloudflarePartialWriteAndLostAcknowledgement(t *testing.T) {
	for _, loseResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(loseResponse), func(t *testing.T) {
			c, f := fixture(t)
			r := testRecord()
			r.Values = []string{"192.0.2.10", "192.0.2.11", "192.0.2.12"}
			f.failAt = 2
			if loseResponse {
				f.failAt = 0
				f.hook = func(w http.ResponseWriter, req *http.Request) bool {
					if req.Method != http.MethodPost || f.mutations != 1 {
						return false
					}
					var entry cfRecord
					_ = json.NewDecoder(req.Body).Decode(&entry)
					f.add(entry)
					f.mutations++
					http.Error(w, "response lost", 502)
					return true
				}
			}
			if err := c.Ensure(t.Context(), r); err == nil {
				t.Fatal("expected interrupted write")
			}
			f.hook = nil
			if err := c.Ensure(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if len(f.entries) != 3 {
				t.Fatal("replay duplicated or lost values", f.entries)
			}
			before := f.mutations
			if err := c.Ensure(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if before != f.mutations {
				t.Fatal("replay still changed DNS")
			}
		})
	}
}

func TestCloudflarePreservesForeignRecords(t *testing.T) {
	for _, kind := range []string{"A", "CNAME", "NS"} {
		t.Run(kind, func(t *testing.T) {
			c, f := fixture(t)
			r := testRecord()
			f.add(cfRecord{Name: r.Name, Type: kind, Content: "192.0.2.10", Comment: "someone else"})
			if err := c.Ensure(t.Context(), r); err == nil {
				t.Fatal("adopted conflicting foreign record")
			}
			if err := c.Delete(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if f.mutations != 0 || len(f.entries) != 1 {
				t.Fatal("changed foreign record")
			}
		})
	}
	// CNAME writes must also inspect non-address types at the same name.
	c, f := fixture(t)
	r := testRecord()
	r.Type, r.Values = "CNAME", []string{"gateway.example.test"}
	f.add(cfRecord{Name: r.Name, Type: "TXT", Content: "existing", Comment: "other"})
	if err := c.Ensure(t.Context(), r); err == nil || f.mutations != 0 {
		t.Fatal("CNAME ignored another record type", err)
	}
}

func TestCloudflareConcurrentTXTAndDuplicateCleanup(t *testing.T) {
	c, f := fixture(t)
	r := Record{ID: "challenge-1", Name: "_acme-challenge.alpha.dispatch.example.test", Type: "TXT", Values: []string{"first"}, TTL: 60}
	other := r
	other.ID, other.Values = "challenge-2", []string{"second"}
	foreign := f.add(cfRecord{Name: r.Name, Type: r.Type, Content: "\"unrelated\"", Comment: "verification", TTL: 60})
	var wg sync.WaitGroup
	for _, record := range []Record{r, other, r, other} {
		wg.Add(1)
		go func(record Record) {
			defer wg.Done()
			if err := c.Ensure(t.Context(), record); err != nil {
				t.Error(err)
			}
		}(record)
	}
	wg.Wait()
	if len(f.entries) != 3 {
		t.Fatal("concurrent TXT records were lost", f.entries)
	}
	f.add(desired(r, "first"))
	f.add(desired(r, "obsolete"))
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 3 {
		t.Fatal("duplicate owned records survived", f.entries)
	}
	f.add(desired(r, "duplicate"))
	if err := c.Delete(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 2 || f.entries[foreign].Comment != "verification" {
		t.Fatal("delete affected another owner", f.entries)
	}
	other.Values = []string{"unrelated"}
	if err := c.Ensure(t.Context(), other); err == nil {
		t.Fatal("adopted foreign TXT value")
	}
}

func TestCloudflareTXTEncodingAndAddressNormalization(t *testing.T) {
	for _, value := range []string{"acme-token", `quoted " and \ slash`, "utf8 café", "tab\there", ""} {
		t.Run(value, func(t *testing.T) {
			c, f := fixture(t)
			r := Record{ID: "txt", Name: "_acme-challenge.dispatch.example.test", Type: "TXT", Values: []string{value}, TTL: 60}
			if err := c.Ensure(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if err := c.Ensure(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if f.mutations != 1 {
				t.Fatal("quoted TXT was not idempotent", f.entries)
			}
		})
	}
	c, f := fixture(t)
	r := testRecord()
	r.Type, r.Values = "AAAA", []string{"2001:0db8:0:0:0:0:0:1"}
	f.add(cfRecord{Name: r.Name, Type: r.Type, Content: "2001:db8::1", Comment: "dispatch:" + r.ID, TTL: r.TTL})
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if f.mutations != 0 {
		t.Fatal("equivalent IPv6 changed")
	}
}

func TestCloudflareZoneBoundaryAndNameservers(t *testing.T) {
	for _, zone := range []string{"example.test", "dispatch.example.test", "other.test", "notdispatch.example.test", "alpha.dispatch.example.test"} {
		t.Run(zone, func(t *testing.T) {
			c, f := fixture(t)
			f.zone = zone
			ns, err := c.Nameservers(t.Context())
			valid := zone == "example.test" || zone == "dispatch.example.test"
			if (err == nil) != valid {
				t.Fatal("wrong zone accepted", err)
			}
			if valid && len(ns) != 2 {
				t.Fatal(ns)
			}
			for _, op := range []func(context.Context, Record) error{c.Ensure, c.Delete} {
				if err := op(t.Context(), testRecord()); (err == nil) != valid {
					t.Fatal(err)
				}
			}
			if !valid && f.mutations != 0 {
				t.Fatal("mutation outside configured zone")
			}
		})
	}
	c, f := fixture(t)
	for _, name := range []string{"example.test", "notdispatch.example.test", "dispatch.example.test.attacker.test", "*.example.test", "bad/name.dispatch.example.test"} {
		r := testRecord()
		r.Name = name
		if err := c.Ensure(t.Context(), r); err == nil {
			t.Fatal("accepted foreign record", name)
		}
		if err := c.Delete(t.Context(), r); err == nil {
			t.Fatal("accepted foreign deletion", name)
		}
	}
	if f.mutations != 0 {
		t.Fatal("invalid request mutated DNS")
	}
}

func TestCloudflareSanitizedFailuresAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusTemporaryRedirect} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, f := fixture(t)
			redirected := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; http.Error(w, "leaked", 500) }))
			defer target.Close()
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Location", target.URL)
				w.Header().Set("Retry-After", "900")
				http.Error(w, testToken+" body-secret", status)
				return true
			}
			start := time.Now()
			err := c.Ensure(t.Context(), testRecord())
			if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "body-secret") || redirected != 0 {
				t.Fatal("unsafe error/redirect", err, redirected)
			}
			if time.Since(start) > time.Second {
				t.Fatal("provider retries blocked reconciliation")
			}
		})
	}
}

func TestCloudflareRejectsInvalidAndOversizedResponses(t *testing.T) {
	for _, body := range []string{`{"success":false,"errors":[{"message":"` + testToken + `"}]}`, `{"success":true,"result":`, strings.Repeat("x", maxResponseBytes+1)} {
		c, f := fixture(t)
		f.hook = func(w http.ResponseWriter, r *http.Request) bool { fmt.Fprint(w, body); return true }
		if _, err := c.Nameservers(t.Context()); err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatal(err)
		}
	}
}

func TestCloudflareOwnershipRecheck(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			c, f := fixture(t)
			r := testRecord()
			id := f.add(desired(r, "192.0.2.11"))
			f.hook = func(w http.ResponseWriter, req *http.Request) bool {
				if req.URL.Path == c.recordPath(id) && req.Method == http.MethodGet {
					entry := f.entries[id]
					entry.Comment = "new owner"
					f.entries[id] = entry
				}
				return false
			}
			op := c.Ensure
			if remove {
				op = c.Delete
			}
			if err := op(t.Context(), r); err == nil {
				t.Fatal("changed record owner was ignored")
			}
			if f.mutations != 0 {
				t.Fatal("mutated another owner's record")
			}
		})
	}
}

func TestCloudflareCancellationAndConfiguration(t *testing.T) {
	c, f := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Ensure(ctx, testRecord()); err == nil {
		t.Fatal("ignored cancellation")
	}
	if f.mutations != 0 {
		t.Fatal("cancelled call mutated DNS")
	}
	for _, cfg := range []CloudflareConfig{
		{ZoneID: testZoneID, RootDomain: "dispatch.example.test"},
		{APIToken: testToken + "\n", ZoneID: testZoneID, RootDomain: "dispatch.example.test"},
		{APIToken: testToken, ZoneID: "../wrong", RootDomain: "dispatch.example.test"},
		{APIToken: testToken, ZoneID: testZoneID, RootDomain: "127.0.0.1"},
	} {
		if _, err := NewCloudflare(cfg); err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatal("invalid configuration or leaked token", err)
		}
	}
}

func TestCloudflarePartialDeleteKeepsOtherOwners(t *testing.T) {
	c, f := fixture(t)
	r := testRecord()
	for range 3 {
		f.add(desired(r, "192.0.2.10"))
	}
	foreign := f.add(cfRecord{Name: r.Name, Type: r.Type, Content: "192.0.2.99", Comment: "unrelated"})
	f.failAt = 2
	if err := c.Delete(t.Context(), r); err == nil {
		t.Fatal("expected deletion failure")
	}
	if err := c.Delete(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 1 || f.entries[foreign].Comment != "unrelated" {
		t.Fatal("cleanup touched another owner", f.entries)
	}
}

func TestCloudflareRepairsTTLAndDisablesProxy(t *testing.T) {
	c, f := fixture(t)
	r := testRecord()
	entry := desired(r, r.Values[0])
	entry.Proxied, entry.TTL = true, 1
	id := f.add(entry)
	if err := c.Ensure(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if f.entries[id].Proxied || f.entries[id].TTL != r.TTL || f.mutations != 1 {
		t.Fatal("owned proxy and TTL were not updated", f.entries)
	}
}

func TestCloudflareRejectsIncompleteRecordLists(t *testing.T) {
	for _, wrongName := range []bool{false, true} {
		c, f := fixture(t)
		r := testRecord()
		f.hook = func(w http.ResponseWriter, req *http.Request) bool {
			if !strings.HasSuffix(req.URL.Path, "/dns_records") {
				return false
			}
			entry := desired(r, r.Values[0])
			entry.ID = testZoneID
			if wrongName {
				entry.Name = "outside.example.test"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []cfRecord{entry}, "result_info": map[string]int{"page": 1, "total_pages": 1, "total_count": 2}})
			return true
		}
		if err := c.Ensure(t.Context(), r); err == nil {
			t.Fatal("incomplete/foreign listing accepted")
		}
		if f.mutations != 0 {
			t.Fatal("mutated records after invalid listing")
		}
	}
}

func TestCloudflareCancelsInFlightRequest(t *testing.T) {
	c, f := fixture(t)
	started := make(chan struct{})
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		close(started)
		<-r.Context().Done()
		return true
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Ensure(ctx, testRecord()) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not honor cancellation")
	}
}

func TestCloudflareTargetIdentifiesZoneWithoutCredentials(t *testing.T) {
	base := CloudflareConfig{APIToken: testToken, ZoneID: testZoneID, RootDomain: "dispatch.example.test"}
	for _, change := range []func(*CloudflareConfig){
		func(c *CloudflareConfig) {},
		func(c *CloudflareConfig) { c.APIToken = "rotated-private-token" },
		func(c *CloudflareConfig) { c.RootDomain = "another.example.test" },
		func(c *CloudflareConfig) { c.ZoneID = strings.ToUpper(c.ZoneID) },
	} {
		cfg := base
		change(&cfg)
		provider, err := NewCloudflare(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if provider.Target() != "cloudflare:"+testZoneID {
			t.Fatal("credential rotation or root configuration changed the publication target", provider.Target())
		}
	}
	base.ZoneID = "11111111111111111111111111111111"
	other, err := NewCloudflare(base)
	if err != nil {
		t.Fatal(err)
	}
	if other.Target() != "cloudflare:"+base.ZoneID {
		t.Fatal("different zone reused the publication target", other.Target())
	}
}
