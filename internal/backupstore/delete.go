package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Delete removes one authenticated, unchanged object. The registered store must
// enforce If-Match on DELETE. Versioned objects require a separate version API.
func Delete(ctx context.Context, client *http.Client, access ObjectAccess, project, backup, store, checksum string, maxBytes int64) error {
	if access.Delete == "" || len(checksum) != 64 {
		return errors.New("conditional object deletion is unavailable")
	}
	head, err := request(ctx, client, "HEAD", access.Head, nil, nil, -1)
	if err != nil {
		return err
	}
	head.Body.Close()
	if versionedDeletionEvidence(head) {
		return errors.New("versioned object cleanup is unsupported")
	}
	if head.StatusCode == 404 {
		return nil
	}
	tag := head.Header.Get("ETag")
	version := head.Header.Get("x-amz-version-id")
	if head.StatusCode != 200 || tag == "" || tag == "*" || strings.ContainsAny(tag, "\r\n") || version != "" && version != "null" || head.ContentLength < 0 || head.ContentLength > maxBytes || head.Header.Get("x-amz-meta-dispatch-project") != project || head.Header.Get("x-amz-meta-dispatch-backup") != backup || head.Header.Get("x-amz-meta-dispatch-store") != store {
		return errors.New("encrypted object deletion identity or conditional support cannot be verified")
	}
	response, err := request(ctx, client, "GET", access.Get, nil, map[string]string{"If-Match": tag}, -1)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, io.LimitReader(response.Body, maxBytes+1))
	response.Body.Close()
	if response.StatusCode != 200 || copyErr != nil || n != head.ContentLength || n > maxBytes || hex.EncodeToString(hash.Sum(nil)) != checksum {
		return errors.New("reviewed encrypted object bytes changed")
	}
	response, deleteErr := request(ctx, client, "DELETE", access.Delete, nil, map[string]string{"If-Match": tag}, -1)
	if response != nil {
		response.Body.Close()
		if response.Header.Get("x-amz-delete-marker") == "true" || response.Header.Get("x-amz-version-id") != "" && response.Header.Get("x-amz-version-id") != "null" {
			return errors.New("versioned object cleanup is unresolved")
		}
		if response.StatusCode != 200 && response.StatusCode != 204 && response.StatusCode != 404 {
			return errors.New("conditional object deletion was rejected")
		}
	}
	// A missing object settles a lost reply. Existing or inaccessible objects keep
	// the operation uncertain and its recovery metadata and protections intact.
	settled, err := request(ctx, client, "HEAD", access.Head, nil, nil, -1)
	if err != nil {
		return errors.New("encrypted object deletion outcome is unresolved")
	}
	settled.Body.Close()
	if settled.StatusCode != 404 || versionedDeletionEvidence(settled) {
		return errors.New("encrypted object deletion outcome is unresolved")
	}

	_ = deleteErr
	return nil
}

func versionedDeletionEvidence(response *http.Response) bool {
	version := response.Header.Get("x-amz-version-id")
	return response.Header.Get("x-amz-delete-marker") == "true" || version != "" && version != "null"
}
