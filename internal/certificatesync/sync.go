// Package certificatesync installs one tenant's workload certificate at an ingress.
package certificatesync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Origin    string
	TokenFile string
	Directory string
	Client    *http.Client
	Roots     *x509.CertPool
	Reload    func(context.Context) error
}

type bundle struct {
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem"`
}

// Sync validates the downloaded bundle before changing current. The ingress
// must read current/fullchain.pem and current/privkey.pem only when reloaded.
// A failed reload restores the previous bundle and attempts to reload it.
func Sync(ctx context.Context, config Config) (bool, error) {
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" || (origin.Port() != "" && origin.Port() != "443") || strings.ContainsAny(origin.Hostname(), ":*") {
		return false, errors.New("origin must be a tenant HTTPS origin")
	}
	if !filepath.IsAbs(config.Directory) || config.Reload == nil {
		return false, errors.New("an absolute certificate directory and reload command are required")
	}
	token, err := readPrivate(config.TokenFile)
	if err != nil {
		return false, fmt.Errorf("read certificate credential: %w", err)
	}
	token = bytes.TrimSpace(token)
	if len(token) < 32 || len(token) > 512 || bytes.ContainsAny(token, " \r\n\t") {
		return false, errors.New("invalid certificate credential")
	}
	if err := os.MkdirAll(config.Directory, 0700); err != nil {
		return false, err
	}
	if err := privateDirectory(config.Directory); err != nil {
		return false, err
	}
	lockPath := filepath.Join(config.Directory, "sync.lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return false, errors.New("certificate lock must be a private regular file")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false, errors.New("another certificate installer is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	value, err := fetch(ctx, config, token)
	if err != nil {
		return false, err
	}
	if err := validate(value, origin.Hostname(), config.Roots); err != nil {
		return false, err
	}
	previous, err := current(config.Directory)
	if err != nil {
		return false, err
	}
	if previous != "" {
		old, err := readBundle(filepath.Join(config.Directory, previous))
		if err != nil {
			return false, err
		}
		if old == value {
			if marker, err := readPrivate(filepath.Join(config.Directory, previous, "activated")); err == nil && string(marker) == "ok\n" {
				return false, nil
			}
			// A crash may leave a validated bundle selected but not yet reloaded.
			return true, activate(ctx, config, previous)
		}
	}
	previous, err = lastActivated(config.Directory, previous)
	if err != nil {
		return false, err
	}
	generation, err := os.MkdirTemp(config.Directory, "bundle-")
	if err != nil {
		return false, err
	}
	name := filepath.Base(generation)
	for file, contents := range map[string]string{"fullchain.pem": value.CertificatePEM, "privkey.pem": value.PrivateKeyPEM, "previous": previous} {
		if err := writePrivate(filepath.Join(generation, file), []byte(contents)); err != nil {
			_ = os.RemoveAll(generation)
			return false, err
		}
	}
	if err := syncDirectory(generation); err != nil {
		_ = os.RemoveAll(generation)
		return false, err
	}
	if err := pointCurrent(config.Directory, name); err != nil {
		return false, err
	}
	return true, activate(ctx, config, name)
}

func fetch(ctx context.Context, config Config, token []byte) (bundle, error) {
	var value bundle
	client := http.Client{Timeout: 30 * time.Second}
	if config.Client != nil {
		client = *config.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("certificate endpoint redirected") }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.Origin+"/api/v1/hosted/certificate", nil)
	if err != nil {
		return value, errors.New("invalid certificate endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return value, errors.New("certificate download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return value, fmt.Errorf("certificate endpoint returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &value) != nil {
		return value, errors.New("invalid certificate response")
	}
	return value, nil
}

func validate(value bundle, host string, roots *x509.CertPool) error {
	pair, err := tls.X509KeyPair([]byte(value.CertificatePEM), []byte(value.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("certificate and key do not match")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("invalid workload certificate")
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "*."+host || len(leaf.IPAddresses) != 0 || len(leaf.URIs) != 0 || len(leaf.EmailAddresses) != 0 {
		return errors.New("certificate does not match this tenant's workload namespace")
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return errors.New("invalid certificate chain")
		}
		intermediates.AddCert(cert)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: "certificate-check." + host, Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		return errors.New("workload certificate is expired, not yet valid, or not trusted")
	}
	return nil
}

func activate(ctx context.Context, config Config, name string) error {
	path := filepath.Join(config.Directory, name)
	previous, err := readPrivate(filepath.Join(path, "previous"))
	if err != nil {
		return err
	}
	if len(previous) != 0 {
		if err := checkGeneration(config.Directory, string(previous)); err != nil {
			return err
		}
	}
	reload, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = config.Reload(reload)
	cancel()
	if err != nil {
		if rollbackErr := pointCurrent(config.Directory, string(previous)); rollbackErr != nil {
			return fmt.Errorf("ingress reload failed; cannot restore previous bundle: %w", rollbackErr)
		}
		if len(previous) != 0 {
			// Rollback must still run if the original request was cancelled.
			rollback, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			rollbackErr := config.Reload(rollback)
			stop()
			if rollbackErr != nil {
				return errors.New("ingress reload failed; previous files restored but their reload also failed")
			}
		}
		return errors.New("ingress reload failed; previous certificate files restored")
	}
	if err := markActivated(path); err != nil {
		return err
	}
	if err := syncDirectory(path); err != nil {
		return err
	}
	// Keep the active and previous bundles for rollback and remove older keys.
	entries, err := os.ReadDir(config.Directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "bundle-") && entry.Name() != name && entry.Name() != string(previous) {
			if err := os.RemoveAll(filepath.Join(config.Directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return syncDirectory(config.Directory)
}

func lastActivated(root, name string) (string, error) {
	seen := map[string]bool{}
	for name != "" {
		if seen[name] {
			return "", errors.New("certificate rollback chain contains a cycle")
		}
		seen[name] = true
		if err := checkGeneration(root, name); err != nil {
			return "", err
		}
		path := filepath.Join(root, name)
		if marker, err := readPrivate(filepath.Join(path, "activated")); err == nil && string(marker) == "ok\n" {
			return name, nil
		}
		previous, err := readPrivate(filepath.Join(path, "previous"))
		if err != nil {
			return "", err
		}
		name = string(previous)
	}
	return "", nil
}

func markActivated(path string) error {
	temporary, err := os.CreateTemp(path, ".activated-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.WriteString("ok\n"); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(path, "activated"))
}

func readBundle(path string) (bundle, error) {
	certificate, err := readPrivate(filepath.Join(path, "fullchain.pem"))
	if err != nil {
		return bundle{}, err
	}
	key, err := readPrivate(filepath.Join(path, "privkey.pem"))
	return bundle{CertificatePEM: string(certificate), PrivateKeyPEM: string(key)}, err
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("certificate credentials must be private regular files")
	}
	return os.ReadFile(path)
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("certificate directories must be private and cannot be symlinks")
	}
	return nil
}

func checkGeneration(root, name string) error {
	if !strings.HasPrefix(name, "bundle-") || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid certificate generation")
	}
	return privateDirectory(filepath.Join(root, name))
}

func current(root string) (string, error) {
	path := filepath.Join(root, "current")
	name, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := checkGeneration(root, name); err != nil {
		return "", err
	}
	return name, nil
}

func writePrivate(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func pointCurrent(root, name string) error {
	path := filepath.Join(root, "current")
	if name == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDirectory(root)
	}
	if err := checkGeneration(root, name); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".switch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	link := filepath.Join(staging, "current")
	if err := os.Symlink(name, link); err != nil {
		return err
	}
	if err := os.Rename(link, path); err != nil {
		return err
	}
	return syncDirectory(root)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
