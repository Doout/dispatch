package dnsprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const cloudflareEndpoint = "https://api.cloudflare.com/client/v4"
const maxResponseBytes = 1 << 20

var cloudflareID = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var recordID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)

type CloudflareConfig struct {
	APIToken, ZoneID, RootDomain string
}

type Cloudflare struct {
	config   CloudflareConfig
	client   *http.Client
	endpoint string
	gate     chan struct{}
}

var _ Provider = (*Cloudflare)(nil)

// Target binds publication to a Cloudflare zone independently of its API token.
func (c *Cloudflare) Target() string { return "cloudflare:" + c.config.ZoneID }

// NewCloudflare validates local configuration without contacting the provider.
func NewCloudflare(cfg CloudflareConfig) (*Cloudflare, error) {
	cfg.RootDomain = canonicalName(cfg.RootDomain)
	cfg.ZoneID = strings.ToLower(cfg.ZoneID)
	if cfg.APIToken == "" || len(cfg.APIToken) > 4096 || strings.IndexFunc(cfg.APIToken, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return nil, errors.New("Cloudflare API token is missing or invalid")
	}
	if !cloudflareID.MatchString(cfg.ZoneID) || !validName(cfg.RootDomain, false) || net.ParseIP(cfg.RootDomain) != nil || !strings.Contains(cfg.RootDomain, ".") {
		return nil, errors.New("Cloudflare requires a zone ID and a DNS root domain")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, IdleConnTimeout: 30 * time.Second}
	return &Cloudflare{config: cfg, endpoint: cloudflareEndpoint, gate: make(chan struct{}, 1), client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Comment string `json:"comment"`
	TTL     uint32 `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

type cfEnvelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

type statusError int

func (e statusError) Error() string {
	return fmt.Sprintf("Cloudflare DNS request failed with HTTP %d", e)
}

func (c *Cloudflare) request(ctx context.Context, method, path string, input any) (cfEnvelope, error) {
	var result cfEnvelope
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return result, errors.New("cannot encode DNS request")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return result, errors.New("cannot create DNS request")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("Cloudflare DNS request could not complete")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, statusError(response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || json.Unmarshal(raw, &result) != nil {
		return result, errors.New("Cloudflare returned an invalid DNS response")
	}
	if !result.Success {
		return result, errors.New("Cloudflare refused the DNS request")
	}
	return result, nil
}

func (c *Cloudflare) zone(ctx context.Context) ([]string, error) {
	envelope, err := c.request(ctx, http.MethodGet, "/zones/"+c.config.ZoneID, nil)
	if err != nil {
		return nil, err
	}
	var zone struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Nameservers []string `json:"name_servers"`
	}
	if json.Unmarshal(envelope.Result, &zone) != nil || zone.ID != c.config.ZoneID || !validName(canonicalName(zone.Name), false) || !within(c.config.RootDomain, canonicalName(zone.Name)) {
		return nil, errors.New("Cloudflare zone does not contain the configured root domain")
	}
	if len(zone.Nameservers) == 0 || len(zone.Nameservers) > 20 {
		return nil, errors.New("Cloudflare zone has no valid authoritative nameservers")
	}
	seen := map[string]bool{}
	var names []string
	for _, name := range zone.Nameservers {
		name = canonicalName(name)
		if !validName(name, false) || net.ParseIP(name) != nil {
			return nil, errors.New("Cloudflare zone has invalid authoritative nameservers")
		}
		if !seen[name] {
			names, seen[name] = append(names, name), true
		}
	}
	return names, nil
}

func (c *Cloudflare) Nameservers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return c.zone(ctx)
}

func (c *Cloudflare) records(ctx context.Context, name string) ([]cfRecord, error) {
	var records []cfRecord
	seen := map[string]bool{}
	for page := 1; page <= 100; page++ {
		query := url.Values{"name.exact": {name}, "page": {strconv.Itoa(page)}, "per_page": {"100"}, "order": {"name"}}
		envelope, err := c.request(ctx, http.MethodGet, "/zones/"+c.config.ZoneID+"/dns_records?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var entries []cfRecord
		if json.Unmarshal(envelope.Result, &entries) != nil || envelope.ResultInfo.Page != page || envelope.ResultInfo.TotalPages < 0 || envelope.ResultInfo.TotalPages > 100 || envelope.ResultInfo.TotalCount < 0 || envelope.ResultInfo.TotalCount > 10000 || len(entries) > 100 {
			return nil, errors.New("Cloudflare returned invalid DNS pagination")
		}
		for _, entry := range entries {
			if !cloudflareID.MatchString(entry.ID) || seen[entry.ID] || canonicalName(entry.Name) != name {
				return nil, errors.New("Cloudflare returned inconsistent DNS records")
			}
			seen[entry.ID] = true
			entry.Name, entry.Type = name, strings.ToUpper(entry.Type)
			records = append(records, entry)
		}
		if page >= envelope.ResultInfo.TotalPages {
			if len(records) != envelope.ResultInfo.TotalCount {
				return nil, errors.New("Cloudflare returned incomplete DNS pagination")
			}
			return records, nil
		}
		if len(entries) == 0 {
			return nil, errors.New("Cloudflare returned incomplete DNS pagination")
		}
	}
	return nil, errors.New("Cloudflare DNS record limit exceeded")
}

func (c *Cloudflare) locked(ctx context.Context, action func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return action(ctx)
}

func (c *Cloudflare) Ensure(ctx context.Context, record Record) error {
	r, err := c.validate(record, true)
	if err != nil {
		return err
	}
	return c.locked(ctx, func(ctx context.Context) error {
		if _, err := c.zone(ctx); err != nil {
			return err
		}
		entries, err := c.records(ctx, r.Name)
		if err != nil {
			return err
		}
		owner := "dispatch:" + r.ID
		var owned []cfRecord
		for _, entry := range entries {
			if entry.Comment == owner && entry.Type == r.Type {
				owned = append(owned, entry)
				continue
			}
			if entry.Type == "CNAME" || r.Type == "CNAME" || entry.Type == "NS" || entry.Type == r.Type && r.Type != "TXT" {
				return errors.New("DNS name has a conflicting record not owned by this Dispatch record")
			}
			if entry.Type == "TXT" && r.Type == "TXT" {
				for _, value := range r.Values {
					if equivalent(r.Type, entry.Content, value) {
						return errors.New("DNS value already belongs to another record")
					}
				}
			}
		}
		sort.Slice(owned, func(i, j int) bool { return owned[i].ID < owned[j].ID })
		used := make([]bool, len(owned))
		var missing []string
		for _, value := range r.Values {
			index := -1
			for i, entry := range owned {
				if !used[i] && equivalent(r.Type, entry.Content, value) {
					index = i
					break
				}
			}
			if index < 0 {
				missing = append(missing, value)
				continue
			}
			used[index] = true
			if owned[index].TTL != r.TTL || owned[index].Proxied {
				if err := c.update(ctx, owned[index], desired(r, value)); err != nil {
					return err
				}
			}
		}
		for _, value := range missing {
			index := -1
			for i := range owned {
				if !used[i] {
					index = i
					break
				}
			}
			if index >= 0 {
				if err := c.update(ctx, owned[index], desired(r, value)); err != nil {
					return err
				}
				used[index] = true
			} else if _, err := c.request(ctx, http.MethodPost, "/zones/"+c.config.ZoneID+"/dns_records", desired(r, value)); err != nil {
				return err
			}
		}
		for i, entry := range owned {
			if !used[i] {
				if err := c.remove(ctx, entry); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (c *Cloudflare) Delete(ctx context.Context, record Record) error {
	r, err := c.validate(record, false)
	if err != nil {
		return err
	}
	return c.locked(ctx, func(ctx context.Context) error {
		if _, err := c.zone(ctx); err != nil {
			return err
		}
		entries, err := c.records(ctx, r.Name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Comment == "dispatch:"+r.ID && entry.Type == r.Type {
				if err := c.remove(ctx, entry); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Recheck ownership before mutating an ID returned by a previous list request.
func (c *Cloudflare) current(ctx context.Context, expected cfRecord) (bool, error) {
	envelope, err := c.request(ctx, http.MethodGet, c.recordPath(expected.ID), nil)
	if err == statusError(http.StatusNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var current cfRecord
	if json.Unmarshal(envelope.Result, &current) != nil || current.ID != expected.ID || canonicalName(current.Name) != expected.Name || current.Type != expected.Type || current.Comment != expected.Comment {
		return false, errors.New("DNS record ownership changed during reconciliation")
	}
	return true, nil
}

func (c *Cloudflare) recordPath(id string) string {
	return "/zones/" + c.config.ZoneID + "/dns_records/" + id
}

func (c *Cloudflare) update(ctx context.Context, old, next cfRecord) error {
	present, err := c.current(ctx, old)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("DNS record disappeared during reconciliation")
	}
	_, err = c.request(ctx, http.MethodPatch, c.recordPath(old.ID), next)
	return err
}

func (c *Cloudflare) remove(ctx context.Context, record cfRecord) error {
	present, err := c.current(ctx, record)
	if err != nil || !present {
		return err
	}
	_, err = c.request(ctx, http.MethodDelete, c.recordPath(record.ID), nil)
	if err == statusError(http.StatusNotFound) {
		return nil
	}
	return err
}

func desired(r Record, value string) cfRecord {
	if r.Type == "TXT" {
		var escaped strings.Builder
		escaped.WriteByte('"')
		for _, b := range []byte(value) {
			switch {
			case b == '"' || b == '\\':
				escaped.WriteByte('\\')
				escaped.WriteByte(b)
			case b < 32 || b >= 127:
				fmt.Fprintf(&escaped, "\\%03d", b)
			default:
				escaped.WriteByte(b)
			}
		}
		escaped.WriteByte('"')
		value = escaped.String()
	}
	return cfRecord{Name: r.Name, Type: r.Type, Content: value, Comment: "dispatch:" + r.ID, TTL: r.TTL}
}

func equivalent(kind, existing, wanted string) bool {
	switch kind {
	case "A", "AAAA":
		a, b := net.ParseIP(existing), net.ParseIP(wanted)
		return a != nil && b != nil && a.Equal(b)
	case "CNAME":
		return canonicalName(existing) == wanted
	case "TXT":
		if !strings.HasPrefix(existing, "\"") {
			return existing == wanted
		}
		decoded, ok := decodeTXT(existing)
		return ok && decoded == wanted
	}
	return false
}

// Cloudflare returns TXT content as quoted RFC 1035 character strings. Adjacent
// strings are one value; decimal escapes represent bytes, not Unicode points.
func decodeTXT(content string) (string, bool) {
	var result strings.Builder
	for content = strings.TrimSpace(content); content != ""; content = strings.TrimSpace(content) {
		if content[0] != '"' {
			return "", false
		}
		content = content[1:]
		closed := false
		for len(content) > 0 {
			b := content[0]
			content = content[1:]
			if b == '"' {
				closed = true
				break
			}
			if b == '\\' {
				if len(content) == 0 {
					return "", false
				}
				if len(content) >= 3 && content[0] >= '0' && content[0] <= '9' && content[1] >= '0' && content[1] <= '9' && content[2] >= '0' && content[2] <= '9' {
					n, _ := strconv.Atoi(content[:3])
					if n > 255 {
						return "", false
					}
					b, content = byte(n), content[3:]
				} else {
					b, content = content[0], content[1:]
				}
			}
			result.WriteByte(b)
		}
		if !closed {
			return "", false
		}
	}
	return result.String(), true
}

func (c *Cloudflare) validate(r Record, values bool) (Record, error) {
	r.Name, r.Type = canonicalName(r.Name), strings.ToUpper(r.Type)
	if !recordID.MatchString(r.ID) || !validName(r.Name, true) || !within(r.Name, c.config.RootDomain) {
		return r, errors.New("DNS record must have an owner and a name inside the configured root domain")
	}
	if r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME" && r.Type != "TXT" {
		return r, errors.New("DNS record type is unsupported")
	}
	if !values {
		return r, nil
	}
	if len(r.Values) == 0 || len(r.Values) > 100 || r.TTL < 60 || r.TTL > 86400 || r.Type == "CNAME" && len(r.Values) != 1 {
		return r, errors.New("DNS record values or TTL are invalid")
	}
	seen := map[string]bool{}
	var normalized []string
	for _, value := range r.Values {
		switch r.Type {
		case "A", "AAAA":
			ip := net.ParseIP(value)
			if ip == nil || (ip.To4() != nil) != (r.Type == "A") {
				return r, errors.New("DNS address is invalid")
			}
			value = ip.String()
		case "CNAME":
			value = canonicalName(value)
			if !validName(value, false) || net.ParseIP(value) != nil {
				return r, errors.New("DNS alias target is invalid")
			}
		case "TXT":
			if len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
				return r, errors.New("DNS TXT value is invalid")
			}
		}
		if !seen[value] {
			normalized, seen[value] = append(normalized, value), true
		}
	}
	r.Values = normalized
	return r, nil
}

func canonicalName(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }
func within(name, root string) bool    { return name == root || strings.HasSuffix(name, "."+root) }

func validName(name string, record bool) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for i, label := range strings.Split(name, ".") {
		if record && i == 0 && label == "*" {
			continue
		}
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, b := range label {
			if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || record && b == '_') {
				return false
			}
		}
	}
	return true
}
