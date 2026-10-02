// Package workloadbackup stores encrypted workload data independently of releases.
package workloadbackup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const chunkSize = 64 << 10
const maxArchiveBytes = int64(256) << 30

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var magic = []byte("DISPATCH-WORKLOAD-1\n")

type Artifact struct {
	ID                string `json:"id"`
	ProjectID         string `json:"projectId"`
	Checksum          string `json:"checksum"`
	PlaintextChecksum string `json:"plaintextChecksum"`
	Bytes             int64  `json:"bytes"`
	PlaintextBytes    int64  `json:"plaintextBytes"`
	ImageID           string `json:"imageId"`
	RequestDigest     string `json:"requestDigest"`
	Encryption        string `json:"encryption"`
}

func Path(root, id string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || !safeID.MatchString(id) {
		return "", errors.New("backup artifact location is unavailable")
	}
	return filepath.Join(root, id), nil
}
func Key() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func aead(key string) (cipher.AEAD, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid backup encryption key")
	}
	defer clear(raw)
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt writes an authenticated sequence with an authenticated end marker.
// The end marker distinguishes a complete archive from a truncated chunk stream.
func Encrypt(dst io.Writer, src io.Reader, key, id string) (string, int64, error) {
	c, err := aead(key)
	if err != nil {
		return "", 0, err
	}
	prefix := make([]byte, 8)
	if _, err = rand.Read(prefix); err != nil {
		return "", 0, err
	}
	if _, err = dst.Write(append(append([]byte{}, magic...), prefix...)); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	buffer := make([]byte, chunkSize)
	defer clear(buffer)
	var total int64
	for index := uint32(0); ; index++ {
		n, readErr := io.ReadFull(src, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", total, readErr
		}
		total += int64(n)
		if total > maxArchiveBytes {
			return "", total, errors.New("backup exceeds the 256 GiB archive limit")
		}
		nonce := make([]byte, 12)
		copy(nonce, prefix)
		binary.BigEndian.PutUint32(nonce[8:], index)
		aad := []byte(fmt.Sprintf("%s:%d:%t", id, index, n == 0))
		sealed := c.Seal(nil, nonce, buffer[:n], aad)
		if err = binary.Write(dst, binary.BigEndian, uint32(len(sealed))); err != nil {
			return "", total, err
		}
		if _, err = dst.Write(sealed); err != nil {
			return "", total, err
		}
		_, _ = h.Write(buffer[:n])
		if n == 0 {
			return hex.EncodeToString(h.Sum(nil)), total, nil
		}
	}
}
func Decrypt(dst io.Writer, src io.Reader, key, id string) (string, int64, error) {
	c, err := aead(key)
	if err != nil {
		return "", 0, err
	}
	header := make([]byte, len(magic)+8)
	if _, err = io.ReadFull(src, header); err != nil || string(header[:len(magic)]) != string(magic) {
		return "", 0, errors.New("backup encryption header is invalid")
	}
	h := sha256.New()
	var total int64
	for index := uint32(0); ; index++ {
		var size uint32
		if binary.Read(src, binary.BigEndian, &size) != nil || size < uint32(c.Overhead()) || size > chunkSize+uint32(c.Overhead()) {
			return "", total, errors.New("backup archive is truncated or corrupt")
		}
		sealed := make([]byte, size)
		if _, err = io.ReadFull(src, sealed); err != nil {
			return "", total, errors.New("backup archive is truncated")
		}
		nonce := make([]byte, 12)
		copy(nonce, header[len(magic):])
		binary.BigEndian.PutUint32(nonce[8:], index)
		plain, err := c.Open(nil, nonce, sealed, []byte(fmt.Sprintf("%s:%d:%t", id, index, size == uint32(c.Overhead()))))
		if err != nil {
			return "", total, errors.New("backup cannot be decrypted or authenticated")
		}
		if len(plain) == 0 {
			var extra [1]byte
			n, e := src.Read(extra[:])
			if n != 0 || e != io.EOF {
				return "", total, errors.New("backup contains unexpected trailing data")
			}
			return hex.EncodeToString(h.Sum(nil)), total, nil
		}
		total += int64(len(plain))
		if total > maxArchiveBytes {
			clear(plain)
			return "", total, errors.New("backup archive exceeds its size limit")
		}
		_, _ = h.Write(plain)
		_, err = dst.Write(plain)
		clear(plain)
		if err != nil {
			return "", total, err
		}
	}
}
func Checksum(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("backup archive is not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxArchiveBytes+(maxArchiveBytes/chunkSize+1)*20+int64(len(magic))+8+1))
	if err != nil {
		return "", n, err
	}
	if n != info.Size() {
		return "", n, errors.New("backup archive exceeds its size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
