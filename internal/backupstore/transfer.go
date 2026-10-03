package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
)

func Client(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}
func request(ctx context.Context, client *http.Client, method, raw string, body io.Reader, headers map[string]string, size int64) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("encrypted object access is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, errors.New("encrypted object request is invalid")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	response, err := Client(client).Do(req)
	if err != nil {
		return nil, errors.New("encrypted object store is unavailable")
	}
	return response, nil
}
func Head(ctx context.Context, client *http.Client, access ObjectAccess, project, backup, store string) (int64, bool, error) {
	response, err := request(ctx, client, "HEAD", access.Head, nil, nil, -1)
	if err != nil {
		return 0, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return 0, false, nil
	}
	if response.StatusCode != 200 || response.ContentLength < 0 || response.Header.Get("x-amz-meta-dispatch-project") != project || response.Header.Get("x-amz-meta-dispatch-backup") != backup || response.Header.Get("x-amz-meta-dispatch-store") != store {
		return 0, false, errors.New("encrypted object identity cannot be verified")
	}
	return response.ContentLength, true, nil
}
func Download(ctx context.Context, client *http.Client, access ObjectAccess, dst io.Writer, project, backup, store string, maxBytes int64) (string, int64, error) {
	n, exists, err := Head(ctx, client, access, project, backup, store)
	if err != nil || !exists || n > maxBytes {
		return "", 0, errors.New("encrypted object is missing, changed or exceeds its limit")
	}
	response, err := request(ctx, client, "GET", access.Get, nil, nil, -1)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", 0, errors.New("encrypted object download failed")
	}
	hash := sha256.New()
	actual, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(response.Body, maxBytes+1))
	if err != nil || actual != n || actual > maxBytes {
		return "", actual, errors.New("encrypted object download is incomplete or oversized")
	}
	return hex.EncodeToString(hash.Sum(nil)), actual, nil
}
func Upload(ctx context.Context, client *http.Client, access ObjectAccess, path, project, backup, store string, maxBytes int64) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, errors.New("encrypted archive is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", 0, errors.New("encrypted archive exceeds its object limit or is unavailable")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, errors.New("encrypted archive cannot be read")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	size, exists, err := Head(ctx, client, access, project, backup, store)
	if err != nil {
		return "", 0, err
	}
	if !exists {
		headers := map[string]string{"if-none-match": "*"}
		for k, v := range access.Headers {
			headers[k] = v
		}
		response, putErr := request(ctx, client, "PUT", access.Put, file, headers, n)
		if response != nil {
			response.Body.Close()
		}
		if putErr != nil || response.StatusCode != 200 && response.StatusCode != 201 {
			size, exists, err = Head(ctx, client, access, project, backup, store)
			if err != nil || !exists {
				return "", 0, errors.New("encrypted object upload outcome is unresolved")
			}
		}
	} else if size != n {
		return "", 0, errors.New("existing encrypted object has a different size")
	}
	// HEAD settles existence after a lost reply; a full read authenticates the exact bytes.
	actual, remoteBytes, err := Download(ctx, client, access, io.Discard, project, backup, store, maxBytes)
	if err != nil || actual != digest || remoteBytes != n {
		return "", 0, errors.New("encrypted object differs from the accepted archive")
	}
	return digest, n, nil
}
