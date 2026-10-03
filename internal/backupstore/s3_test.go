package backupstore

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSigV4MatchesPublishedS3Example(t *testing.T) {
	// AWS publishes this complete expected signature, independent of this signer.
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	credential := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	raw, err := Presign("https://examplebucket.s3.amazonaws.com/test.txt", "GET", "us-east-1", credential, nil, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Get("X-Amz-Signature") != "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404" {
		t.Fatal("published S3 signature does not match")
	}
}
func TestObjectAccessIsScopedAndNeverOverwrites(t *testing.T) {
	config := Config{Endpoint: "https://storage.example.com", Bucket: "dispatch-backups", Region: "us-east-1", Prefix: "retained/workloads", MaxBytes: 1 << 30}
	now := time.Now().UTC()
	credential := Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret", SessionToken: "session+token/fixture="}
	grant, err := Grant(config, credential, "store-id", "project-id", "backup-id", true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(grant.Archive.Put)
	if u.Path != "/dispatch-backups/retained/workloads/project-id/backup-id/archive.enc" || u.Query().Get("X-Amz-Expires") != "3600" || !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "if-none-match") || u.Query().Get("X-Amz-Security-Token") != credential.SessionToken || grant.Archive.Delete != "" {
		t.Fatal("grant did not freeze ownership, create-only upload or lifetime")
	}
	if strings.Contains(grant.Archive.Put, credential.SecretAccessKey) {
		t.Fatal("secret signing key leaked")
	}
	config.MaxBytes = MaxObjectBytes + 1
	if _, err = Grant(config, credential, "store-id", "project-id", "backup-id", true, false, now); err == nil {
		t.Fatal("single PUT limit ignored")
	}
}
