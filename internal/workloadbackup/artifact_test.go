package workloadbackup

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuthenticatedArchiveRejectsCorruptionTruncationWrongKeyAndIdentity(t *testing.T) {
	key, _ := Key()
	wrong, _ := Key()
	plain := []byte(strings.Repeat("database-row\n", 16000))
	var encrypted bytes.Buffer
	digest, n, err := Encrypt(&encrypted, bytes.NewReader(plain), key, "backup-1")
	if err != nil || n != int64(len(plain)) {
		t.Fatal(n, err)
	}
	var restored bytes.Buffer
	got, n, err := Decrypt(&restored, bytes.NewReader(encrypted.Bytes()), key, "backup-1")
	if err != nil || got != digest || n != int64(len(plain)) || !bytes.Equal(restored.Bytes(), plain) {
		t.Fatal("roundtrip", err)
	}
	raw := encrypted.Bytes()
	corrupt := append([]byte{}, raw...)
	corrupt[len(corrupt)/2] ^= 1
	for _, tc := range []struct {
		name    string
		data    []byte
		key, id string
	}{{"corrupt", corrupt, key, "backup-1"}, {"truncated", raw[:len(raw)-20], key, "backup-1"}, {"wrong key", raw, wrong, "backup-1"}, {"wrong identity", raw, key, "backup-2"}, {"trailing", append(append([]byte{}, raw...), 0), key, "backup-1"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(tc.data), tc.key, tc.id); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	if bytes.Contains(raw, plain[:100]) {
		t.Fatal("archive exposed plaintext")
	}
}
