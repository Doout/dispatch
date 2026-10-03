// Package backupstore grants bounded access to encrypted workload backup objects.
package backupstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MaxObjectBytes int64 = 4 << 30

var safeComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}
type Config struct {
	Endpoint          string `json:"endpoint"`
	Bucket            string `json:"bucket"`
	Region            string `json:"region"`
	Prefix            string `json:"prefix"`
	MaxBytes          int64  `json:"maxBytes"`
	ConditionalDelete bool   `json:"conditionalDelete,omitempty"`
}
type ObjectAccess struct {
	Key     string            `json:"key"`
	Get     string            `json:"get"`
	Head    string            `json:"head"`
	Put     string            `json:"put,omitempty"`
	Delete  string            `json:"delete,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}
type Access struct {
	StoreID   string       `json:"storeId"`
	MaxBytes  int64        `json:"maxBytes"`
	Archive   ObjectAccess `json:"archive"`
	Manifest  ObjectAccess `json:"manifest"`
	ExpiresAt time.Time    `json:"expiresAt"`
}

func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !safeComponent.MatchString(c.Bucket) || !regionPattern.MatchString(c.Region) || c.MaxBytes < 1 || c.MaxBytes > MaxObjectBytes {
		return errors.New("object store requires a bare HTTPS endpoint, bucket, region and a size limit between 1 byte and 4 GiB")
	}
	if c.Prefix == "" || strings.HasPrefix(c.Prefix, "/") || strings.HasSuffix(c.Prefix, "/") {
		return errors.New("object prefix is invalid")
	}
	for _, part := range strings.Split(c.Prefix, "/") {
		if !safeComponent.MatchString(part) || part == "." || part == ".." {
			return errors.New("object prefix is invalid")
		}
	}
	return nil
}
func (c Credentials) Validate() error {
	if len(c.AccessKeyID) < 3 || len(c.AccessKeyID) > 128 || strings.ContainsAny(c.AccessKeyID, "\r\n /\\") || len(c.SecretAccessKey) < 8 || len(c.SecretAccessKey) > 4096 || strings.ContainsAny(c.SessionToken, "\r\n") {
		return errors.New("object-store credential is invalid")
	}
	return nil
}
func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func queryString(v url.Values) string { return strings.ReplaceAll(v.Encode(), "+", "%20") }

// Presign uses SigV4 query authentication. Headers are fixed before issuing the grant.
func Presign(raw, method, region string, credential Credentials, headers map[string]string, now time.Time, ttl time.Duration) (string, error) {
	if err := credential.Validate(); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || !regionPattern.MatchString(region) || ttl < time.Second || ttl > 7*24*time.Hour {
		return "", errors.New("object access cannot be signed")
	}
	switch method {
	case "GET", "HEAD", "PUT", "DELETE":
	default:
		return "", errors.New("object operation cannot be signed")
	}
	normalized := map[string]string{"host": u.Host}
	for k, v := range headers {
		key := strings.ToLower(k)
		if strings.ContainsAny(key+v, "\r\n") || key == "host" {
			return "", errors.New("object access headers are invalid")
		}
		normalized[key] = strings.Join(strings.Fields(v), " ")
	}
	names := []string{}
	for k := range normalized {
		names = append(names, k)
	}
	sort.Strings(names)
	canonical := ""
	for _, name := range names {
		canonical += name + ":" + normalized[name] + "\n"
	}
	signed := strings.Join(names, ";")
	day := now.UTC().Format("20060102")
	scope := day + "/" + region + "/s3/aws4_request"
	q := url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA256"}, "X-Amz-Credential": {credential.AccessKeyID + "/" + scope}, "X-Amz-Date": {now.UTC().Format("20060102T150405Z")}, "X-Amz-Expires": {formatSeconds(ttl)}, "X-Amz-SignedHeaders": {signed}}
	if credential.SessionToken != "" {
		q.Set("X-Amz-Security-Token", credential.SessionToken)
	}
	request := method + "\n" + u.EscapedPath() + "\n" + queryString(q) + "\n" + canonical + "\n" + signed + "\nUNSIGNED-PAYLOAD"
	digest := sha256.Sum256([]byte(request))
	stringToSign := "AWS4-HMAC-SHA256\n" + q.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	signing := hmacSHA(hmacSHA(hmacSHA(hmacSHA([]byte("AWS4"+credential.SecretAccessKey), day), region), "s3"), "aws4_request")
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA(signing, stringToSign)))
	clear(signing)
	u.RawQuery = queryString(q)
	return u.String(), nil
}
func formatSeconds(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
func Grant(c Config, credential Credentials, store, project, backup string, write, remove bool, now time.Time) (Access, error) {
	if err := c.Validate(); err != nil {
		return Access{}, err
	}
	if remove && !c.ConditionalDelete {
		return Access{}, errors.New("destination has not enabled conditional unversioned object deletion")
	}
	if !safeComponent.MatchString(project) || !safeComponent.MatchString(backup) || !safeComponent.MatchString(store) {
		return Access{}, errors.New("object ownership identity is invalid")
	}
	access := Access{StoreID: store, MaxBytes: c.MaxBytes, ExpiresAt: now.Add(time.Hour)}
	for _, item := range []struct {
		name string
		dst  *ObjectAccess
	}{{"archive.enc", &access.Archive}, {"manifest.enc", &access.Manifest}} {
		key := c.Prefix + "/" + project + "/" + backup + "/" + item.name
		u, _ := url.Parse(c.Endpoint)
		u.Path = "/" + c.Bucket + "/" + key
		headers := map[string]string{"x-amz-meta-dispatch-project": project, "x-amz-meta-dispatch-backup": backup, "x-amz-meta-dispatch-store": store}
		item.dst.Key, item.dst.Headers = key, headers
		var err error
		item.dst.Get, err = Presign(u.String(), http.MethodGet, c.Region, credential, nil, now, time.Hour)
		if err != nil {
			return Access{}, err
		}
		item.dst.Head, err = Presign(u.String(), http.MethodHead, c.Region, credential, nil, now, time.Hour)
		if err != nil {
			return Access{}, err
		}
		if write {
			writeHeaders := map[string]string{"if-none-match": "*"}
			for k, v := range headers {
				writeHeaders[k] = v
			}
			item.dst.Put, err = Presign(u.String(), http.MethodPut, c.Region, credential, writeHeaders, now, time.Hour)
			if err != nil {
				return Access{}, err
			}
		}

		if remove {
			item.dst.Delete, err = Presign(u.String(), http.MethodDelete, c.Region, credential, nil, now, time.Hour)
			if err != nil {
				return Access{}, err
			}
		}
	}
	return access, nil
}
