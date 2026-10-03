package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type afterDeleteReply struct{ base http.RoundTripper }

func (r afterDeleteReply) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := r.base.RoundTrip(req)
	if err == nil && req.Method == "DELETE" {
		response.Body.Close()
		return nil, errors.New("lost response")
	}
	return response, err
}
func TestConditionalOwnedObjectDeletion(t *testing.T) {
	for _, mode := range []string{"normal", "lost-reply", "changed-checksum", "changed-before-delete", "foreign-owner", "versioned", "missing-etag", "unsupported", "already-missing", "missing-versioned", "missing-marker", "settlement-marker"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("encrypted fixture")
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			exists := mode != "already-missing" && mode != "missing-versioned" && mode != "missing-marker"
			deletes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !exists {
					if mode == "missing-versioned" {
						w.Header().Set("x-amz-version-id", "retained-version")
					}
					if mode == "missing-marker" || mode == "settlement-marker" {
						w.Header().Set("x-amz-delete-marker", "true")
					}
					w.WriteHeader(404)
					return
				}
				etag := `"accepted"`
				if mode == "changed-before-delete" && r.Method == "DELETE" {
					etag = `"replacement"`
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				w.Header().Set("x-amz-meta-dispatch-project", "project")
				w.Header().Set("x-amz-meta-dispatch-backup", "backup")
				w.Header().Set("x-amz-meta-dispatch-store", "store")
				if mode == "foreign-owner" {
					w.Header().Set("x-amz-meta-dispatch-backup", "foreign")
				}
				if mode == "versioned" {
					w.Header().Set("x-amz-version-id", "v1")
				}
				if mode != "missing-etag" {
					w.Header().Set("ETag", etag)
				}
				switch r.Method {
				case "HEAD":
				case "GET":
					if r.Header.Get("If-Match") != etag {
						w.WriteHeader(412)
						return
					}
					if mode == "changed-checksum" {
						io.WriteString(w, strings.Repeat("x", len(data)))
					} else {
						w.Write(data)
					}
				case "DELETE":
					deletes++
					if mode == "unsupported" {
						w.WriteHeader(501)
						return
					}
					if r.Header.Get("If-Match") != etag {
						w.WriteHeader(412)
						return
					}
					exists = false
					w.Header().Del("Content-Length")
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected object operation %s", r.Method)
				}
			}))
			defer server.Close()
			access, err := Grant(Config{Endpoint: server.URL, Bucket: "backup", Region: "us-east-1", Prefix: "owned", MaxBytes: 100, ConditionalDelete: true}, Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture-secret"}, "store", "project", "backup", false, true, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			client := server.Client()
			if mode == "lost-reply" {
				client = &http.Client{Transport: afterDeleteReply{client.Transport}}
			}
			err = Delete(context.Background(), client, access.Archive, "project", "backup", "store", digest, 100)
			success := mode == "normal" || mode == "lost-reply" || mode == "already-missing"
			if (err == nil) != success {
				t.Fatalf("unexpected outcome %v", err)
			}
			if success && exists {
				t.Fatal("deletion falsely confirmed")
			}
			if !success && !exists && mode != "missing-versioned" && mode != "missing-marker" && mode != "settlement-marker" {
				t.Fatal("changed or unowned object was removed")
			}
			if mode == "changed-checksum" || mode == "foreign-owner" || mode == "versioned" || mode == "missing-etag" {
				if deletes != 0 {
					t.Fatal("unsafe DELETE reached provider")
				}
			}
		})
	}
}
func TestConditionalDeletionRequiresRegisteredSupport(t *testing.T) {
	c := Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "owned", MaxBytes: 100}
	creds := Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture-secret"}
	if _, err := Grant(c, creds, "store", "project", "backup", false, true, time.Now()); err == nil {
		t.Fatal("unverified provider conditional support accepted")
	}
	if _, err := Grant(c, creds, "store", "project", "backup", true, false, time.Now()); err != nil {
		t.Fatal("legacy export was blocked", err)
	}
}
