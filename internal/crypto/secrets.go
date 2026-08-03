package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

type Vault struct {
	aead cipherAEAD
}

type cipherAEAD interface {
	NonceSize() int
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
}

func OpenFile(path string) (*Vault, error) {
	if path == "" {
		return nil, nil
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key = []byte(strings.TrimSpace(string(key)))
	if decoded, decodeErr := base64.StdEncoding.DecodeString(string(key)); decodeErr == nil {
		key = decoded
	}
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("master key must decode to %d bytes", chacha20poly1305.KeySize)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Encrypt(scope string, plaintext []byte) (string, error) {
	if v == nil || v.aead == nil {
		return "", errors.New("secret vault is not configured")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := v.aead.Seal(nil, nonce, plaintext, []byte(scope))
	payload := append(nonce, sealed...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (v *Vault) Decrypt(scope, encoded string) ([]byte, error) {
	if v == nil || v.aead == nil {
		return nil, errors.New("secret vault is not configured")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(payload) < v.aead.NonceSize() {
		return nil, errors.New("encrypted secret is truncated")
	}
	nonce, ciphertext := payload[:v.aead.NonceSize()], payload[v.aead.NonceSize():]
	return v.aead.Open(nil, nonce, ciphertext, []byte(scope))
}
