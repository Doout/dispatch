package backupstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const transferObjectPath = "/fixture-bucket/retained/project-id/backup-id/archive.enc"

type transferFixture struct {
	mu            sync.Mutex
	server        *httptest.Server
	access        ObjectAccess
	object        []byte
	exists        bool
	metadata      map[string]string
	requests      map[string]int
	hideFirstHead bool
	dropPutReply  bool
	rejectPut     bool
	corruptPut    bool
	getBody       []byte
	headSize      *int64
}

func newTransferFixture(t *testing.T) *transferFixture {
	t.Helper()
	f := &transferFixture{
		metadata: map[string]string{"x-amz-meta-dispatch-project": "project-id", "x-amz-meta-dispatch-backup": "backup-id", "x-amz-meta-dispatch-store": "store-id"},
		requests: make(map[string]int),
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests[r.Method]++
		if r.URL.Path != transferObjectPath {
			t.Errorf("object path escaped its scope: %q", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodHead:
			if !f.exists || f.hideFirstHead && f.requests[r.Method] == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for key, value := range f.metadata {
				w.Header().Set(key, value)
			}
			size := int64(len(f.object))
			if f.headSize != nil {
				size = *f.headSize
			}
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		case http.MethodGet:
			if !f.exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body := f.object
			if f.getBody != nil {
				body = f.getBody
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" || !strings.Contains(r.URL.Query().Get("X-Amz-SignedHeaders"), "if-none-match") {
				t.Error("upload did not require an absent object with a signed condition")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			for key, value := range f.metadata {
				if r.Header.Get(key) != value {
					t.Errorf("upload changed ownership header %q", key)
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
			if f.exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || r.ContentLength != int64(len(body)) {
				t.Errorf("upload length was not exact: %d, %d, %v", r.ContentLength, len(body), err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !f.rejectPut {
				f.object, f.exists = body, true
				if f.corruptPut && len(f.object) > 0 {
					f.object[0] ^= 0xff
				}
			}
			if f.dropPutReply {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("drop PUT reply: %v", err)
					return
				}
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected object mutation: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.server.Close)
	grant, err := Grant(Config{Endpoint: f.server.URL, Bucket: "fixture-bucket", Region: "us-east-1", Prefix: "retained", MaxBytes: 1024}, Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, "store-id", "project-id", "backup-id", true, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.access = grant.Archive
	return f
}

func transferArchive(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.enc")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *transferFixture) upload(t *testing.T, path string, limit int64) (string, int64, error) {
	t.Helper()
	return Upload(t.Context(), f.server.Client(), f.access, path, "project-id", "backup-id", "store-id", limit)
}

func TestTransferUploadIsImmutableAndReplayVerifiesBytes(t *testing.T) {
	f := newTransferFixture(t)
	body := []byte("encrypted immutable archive")
	path := transferArchive(t, body)
	for attempt := range 2 {
		digest, size, err := f.upload(t, path, int64(len(body)))
		if err != nil || digest != fmt.Sprintf("%x", sha256.Sum256(body)) || size != int64(len(body)) {
			t.Fatalf("upload %d: digest=%s size=%d err=%v", attempt, digest, size, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests[http.MethodPut] != 1 || f.requests[http.MethodGet] != 2 || !bytes.Equal(f.object, body) {
		t.Fatalf("replay rewrote or did not verify the immutable object: requests=%v", f.requests)
	}
}

func TestTransferUploadRejectsExistingConflictsWithoutWriting(t *testing.T) {
	body := []byte("encrypted archive")
	for _, field := range []string{"project", "backup", "store", "size", "bytes"} {
		t.Run(field, func(t *testing.T) {
			f := newTransferFixture(t)
			f.object, f.exists = bytes.Clone(body), true
			switch field {
			case "size":
				f.object = append(f.object, '!')
			case "bytes":
				f.object[0] ^= 0xff
			default:
				f.metadata["x-amz-meta-dispatch-"+field] = "another-owner"
			}
			before := bytes.Clone(f.object)
			if _, _, err := f.upload(t, transferArchive(t, body), 1024); err == nil {
				t.Fatal("accepted a conflicting object")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.requests[http.MethodPut] != 0 || !bytes.Equal(f.object, before) {
				t.Fatal("conflicting object was overwritten")
			}
		})
	}
}

func TestTransferConditionalPutRaceRequiresExactWinner(t *testing.T) {
	for _, matches := range []bool{true, false} {
		t.Run(strconv.FormatBool(matches), func(t *testing.T) {
			f := newTransferFixture(t)
			body := []byte("encrypted archive")
			f.object, f.exists, f.hideFirstHead = bytes.Clone(body), true, true
			if !matches {
				f.object[0] ^= 0xff
			}
			before := bytes.Clone(f.object)
			_, _, err := f.upload(t, transferArchive(t, body), 1024)
			if (err == nil) != matches {
				t.Fatalf("conditional PUT conflict: matching=%t err=%v", matches, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.requests[http.MethodPut] != 1 || !bytes.Equal(f.object, before) {
				t.Fatal("conditional PUT replaced its winner")
			}
		})
	}
}

func TestTransferLostPutReplyRecoversOnlyExactCommittedBytes(t *testing.T) {
	for _, outcome := range []string{"committed", "corrupted", "absent"} {
		t.Run(outcome, func(t *testing.T) {
			f := newTransferFixture(t)
			f.dropPutReply, f.corruptPut, f.rejectPut = true, outcome == "corrupted", outcome == "absent"
			body := []byte("encrypted archive")
			digest, size, err := f.upload(t, transferArchive(t, body), 1024)
			if outcome == "committed" {
				if err != nil || digest != fmt.Sprintf("%x", sha256.Sum256(body)) || size != int64(len(body)) {
					t.Fatalf("lost reply was not recovered by reading committed bytes: %v", err)
				}
			} else if err == nil {
				t.Fatal("lost reply falsely reported a complete upload")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.requests[http.MethodPut] != 1 || f.requests[http.MethodHead] < 2 {
				t.Fatalf("recovery repeated PUT or skipped inspection: %v", f.requests)
			}
		})
	}
}

func TestTransferLimitsBoundLocalAndRemoteBytes(t *testing.T) {
	t.Run("oversized local archive", func(t *testing.T) {
		f := newTransferFixture(t)
		if _, _, err := f.upload(t, transferArchive(t, []byte("12345")), 4); err == nil {
			t.Fatal("oversized local archive accepted")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.requests) != 0 {
			t.Fatal("oversized archive reached object storage")
		}
	})
	for _, scenario := range []string{"head too large", "get too large", "get truncated", "exact limit"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransferFixture(t)
			f.object, f.exists = []byte("1234"), true
			switch scenario {
			case "head too large":
				n := int64(5)
				f.headSize = &n
			case "get too large":
				f.getBody = bytes.Repeat([]byte("!"), 1024)
			case "get truncated":
				f.getBody = []byte("123")
			}
			var dst bytes.Buffer
			digest, size, err := Download(t.Context(), f.server.Client(), f.access, &dst, "project-id", "backup-id", "store-id", 4)
			if scenario == "exact limit" {
				if err != nil || size != 4 || digest != fmt.Sprintf("%x", sha256.Sum256(f.object)) || !bytes.Equal(dst.Bytes(), f.object) {
					t.Fatalf("exact limit rejected: %v", err)
				}
			} else if err == nil || dst.Len() > 5 {
				t.Fatalf("unbounded or accepted invalid download: written=%d err=%v", dst.Len(), err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if scenario == "head too large" && (f.requests[http.MethodGet] != 0 || dst.Len() != 0) {
				t.Fatal("known oversized object was downloaded")
			}
		})
	}
}

func TestTransferNeverFollowsObjectRedirects(t *testing.T) {
	for _, method := range []string{http.MethodHead, http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			var forwarded, redirects atomic.Int32
			destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == method {
					w.Header().Set("Location", destination.URL+"/stolen?credential=fixture-token")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				if method == http.MethodPut {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("x-amz-meta-dispatch-project", "project-id")
				w.Header().Set("x-amz-meta-dispatch-backup", "backup-id")
				w.Header().Set("x-amz-meta-dispatch-store", "store-id")
				w.Header().Set("Content-Length", "4")
			}))
			defer source.Close()
			client := source.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { redirects.Add(1); return nil }
			access := ObjectAccess{Head: source.URL, Get: source.URL, Put: source.URL}
			var err error
			if method == http.MethodPut {
				_, _, err = Upload(t.Context(), client, access, transferArchive(t, []byte("1234")), "project-id", "backup-id", "store-id", 4)
			} else {
				_, _, err = Download(t.Context(), client, access, io.Discard, "project-id", "backup-id", "store-id", 4)
			}
			if err == nil || forwarded.Load() != 0 || redirects.Load() != 0 {
				t.Fatalf("object redirect was accepted: forwarded=%d callbacks=%d err=%v", forwarded.Load(), redirects.Load(), err)
			}
			if client.CheckRedirect == nil {
				t.Fatal("transfer modified the caller's HTTP client")
			}
		})
	}
}

func TestTransferRejectsUnscopedTransportBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, endpoint := range []string{server.URL, "https://user:password@127.0.0.1/object", "https://127.0.0.1/object#fragment"} {
		if _, _, err := Head(context.Background(), server.Client(), ObjectAccess{Head: endpoint}, "project-id", "backup-id", "store-id"); err == nil {
			t.Fatal("unsafe object URL was accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("object credentials were sent over plaintext transport")
	}
}
