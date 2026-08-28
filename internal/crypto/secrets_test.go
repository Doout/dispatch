package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultRoundTripAndScopeBinding(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}

	vault, err := OpenFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Encrypt("github:example", []byte("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := vault.Decrypt("github:example", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "private-token" {
		t.Fatalf("unexpected plaintext %q", opened)
	}
	if _, err := vault.Decrypt("provider:private", sealed); err == nil {
		t.Fatal("ciphertext decrypted under the wrong scope")
	}
}

func TestOpenFileRejectsWrongKeySize(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(keyFile, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(keyFile); err == nil {
		t.Fatal("expected an invalid key error")
	}
}
