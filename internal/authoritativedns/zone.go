// Package authoritativedns serves a controller-owned zone without recursion.
package authoritativedns

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"

	"github.com/miekg/dns"
)

// Snapshot is the complete desired zone. Generation increases on every change.
// Record IDs and generations make retries safe without permitting stale owners
// to replace another owner's records. The catalog must authorize each change.
type Snapshot struct {
	Zone         string   `json:"zone"`
	Generation   uint64   `json:"generation"`
	Serial       uint32   `json:"serial"`
	Nameservers  []string `json:"nameservers"`
	AdminMailbox string   `json:"adminMailbox"`
	TTL          uint32   `json:"ttl"`
	Records      []Record `json:"records"`
}

type Record struct {
	ID         string   `json:"id"`
	OwnerID    string   `json:"ownerId"`
	Generation uint64   `json:"generation"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Values     []string `json:"values"`
	TTL        uint32   `json:"ttl"`
}

type zone struct {
	snapshot Snapshot
	encoded  []byte
	nodes    map[string]map[uint16][]dns.RR
	soa      *dns.SOA
}

func canonical(raw string, wildcard bool) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(raw, "."))
	if name == "" || len(name) > 253 {
		return "", errors.New("invalid DNS name")
	}
	for i, label := range strings.Split(name, ".") {
		if wildcard && i == 0 && label == "*" {
			continue
		}
		if label == "" || len(label) > 63 {
			return "", errors.New("invalid DNS label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", errors.New("invalid DNS label")
			}
		}
	}
	return name + ".", nil
}

func compile(snapshot Snapshot) (*zone, error) {
	snapshot.Nameservers = append([]string(nil), snapshot.Nameservers...)
	snapshot.Records = append([]Record(nil), snapshot.Records...)
	name, err := canonical(snapshot.Zone, false)
	if err != nil {
		return nil, err
	}
	if snapshot.Generation == 0 || len(snapshot.Nameservers) == 0 || len(snapshot.Records) > 100000 {
		return nil, errors.New("zone requires a generation, nameservers, and at most 100000 records")
	}
	if snapshot.TTL == 0 {
		snapshot.TTL = 300
	}
	if snapshot.TTL > 86400 {
		return nil, errors.New("TTL exceeds one day")
	}
	if snapshot.Serial == 0 {
		snapshot.Serial = uint32(snapshot.Generation)
	}
	snapshot.Zone = name
	z := &zone{snapshot: snapshot, nodes: map[string]map[uint16][]dns.RR{name: {}}}
	for i, ns := range snapshot.Nameservers {
		ns, err = canonical(ns, false)
		if err != nil {
			return nil, err
		}
		snapshot.Nameservers[i] = ns
		z.nodes[name][dns.TypeNS] = append(z.nodes[name][dns.TypeNS], &dns.NS{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: snapshot.TTL}, Ns: ns})
	}
	mailbox := snapshot.AdminMailbox
	if mailbox == "" {
		mailbox = "hostmaster." + name
	}
	mailbox, err = canonical(mailbox, false)
	if err != nil {
		return nil, err
	}
	snapshot.AdminMailbox = mailbox
	z.soa = &dns.SOA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60}, Ns: snapshot.Nameservers[0], Mbox: mailbox, Serial: snapshot.Serial, Refresh: 300, Retry: 60, Expire: 86400, Minttl: 60}
	z.nodes[name][dns.TypeSOA] = []dns.RR{z.soa}
	ids := map[string]bool{}
	for i, r := range snapshot.Records {
		if r.ID == "" || r.OwnerID == "" || r.Generation == 0 || ids[r.ID] || len(r.Values) == 0 || len(r.Values) > 1024 {
			return nil, errors.New("records require unique IDs, ownership, generation, and values")
		}
		ids[r.ID] = true
		r.Name, err = canonical(r.Name, true)
		if err != nil || !dns.IsSubDomain(name, r.Name) {
			return nil, errors.New("record is outside the authoritative zone")
		}
		r.Type = strings.ToUpper(r.Type)
		if r.TTL == 0 {
			r.TTL = snapshot.TTL
		}
		if r.TTL > 86400 {
			return nil, errors.New("TTL exceeds one day")
		}
		if z.nodes[r.Name] == nil {
			z.nodes[r.Name] = map[uint16][]dns.RR{}
		}
		if r.Type == "CNAME" && (r.Name == name || len(r.Values) != 1) {
			return nil, errors.New("CNAME requires one value and cannot replace the zone apex")
		}
		if r.Type == "NS" && r.Name == name {
			return nil, errors.New("configure apex NS through nameservers")
		}
		if r.Type == "NS" && strings.HasPrefix(r.Name, "*.") {
			return nil, errors.New("child delegations require an exact DNS name")
		}
		for _, value := range r.Values {
			h := dns.RR_Header{Name: r.Name, Class: dns.ClassINET, Ttl: r.TTL}
			var rr dns.RR
			switch r.Type {
			case "A":
				ip := net.ParseIP(value)
				if ip == nil || ip.To4() == nil {
					return nil, errors.New("invalid IPv4 address")
				}
				h.Rrtype = dns.TypeA
				rr = &dns.A{Hdr: h, A: ip.To4()}
			case "AAAA":
				ip := net.ParseIP(value)
				if ip == nil || ip.To4() != nil {
					return nil, errors.New("invalid IPv6 address")
				}
				h.Rrtype = dns.TypeAAAA
				rr = &dns.AAAA{Hdr: h, AAAA: ip}
			case "CNAME", "NS":
				target, e := canonical(value, false)
				if e != nil {
					return nil, e
				}
				if r.Type == "CNAME" {
					h.Rrtype = dns.TypeCNAME
					rr = &dns.CNAME{Hdr: h, Target: target}
				} else {
					h.Rrtype = dns.TypeNS
					rr = &dns.NS{Hdr: h, Ns: target}
				}
			case "TXT":
				if len(value) > 16384 {
					return nil, errors.New("TXT value exceeds 16 KiB")
				}
				h.Rrtype = dns.TypeTXT
				parts := []string{}
				for len(value) > 255 {
					parts = append(parts, value[:255])
					value = value[255:]
				}
				parts = append(parts, value)
				rr = &dns.TXT{Hdr: h, Txt: parts}
			default:
				return nil, fmt.Errorf("unsupported record type %q", r.Type)
			}
			z.nodes[r.Name][rr.Header().Rrtype] = append(z.nodes[r.Name][rr.Header().Rrtype], rr)
		}
		snapshot.Records[i] = r
		for parent := parentName(r.Name); dns.IsSubDomain(name, parent); parent = parentName(parent) {
			if z.nodes[parent] == nil {
				z.nodes[parent] = map[uint16][]dns.RR{}
			}
			if parent == name {
				break
			}
		}
	}
	for _, sets := range z.nodes {
		for _, records := range sets {
			ttl := records[0].Header().Ttl
			for _, rr := range records {
				if rr.Header().Ttl < ttl {
					ttl = rr.Header().Ttl
				}
			}
			for _, rr := range records {
				rr.Header().Ttl = ttl
			}
		}
		if len(sets[dns.TypeCNAME]) > 0 && (len(sets) != 1 || len(sets[dns.TypeCNAME]) != 1) {
			return nil, errors.New("CNAME conflicts with another record")
		}
	}
	z.snapshot = snapshot
	z.encoded, err = json.Marshal(snapshot)
	return z, err
}

func parentName(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return ""
}

func validateChange(old, next *zone) error {
	if old == nil {
		return nil
	}
	if old.snapshot.Zone != next.snapshot.Zone {
		return errors.New("cannot replace the delegated zone")
	}
	if next.snapshot.Generation < old.snapshot.Generation {
		return errors.New("stale DNS snapshot")
	}
	if next.snapshot.Generation == old.snapshot.Generation {
		if reflect.DeepEqual(old.snapshot, next.snapshot) {
			return nil
		}
		return errors.New("DNS generation already has different content")
	}
	if delta := next.snapshot.Serial - old.snapshot.Serial; delta == 0 || delta >= 1<<31 {
		return errors.New("SOA serial must advance")
	}
	previous := map[string]Record{}
	for _, r := range old.snapshot.Records {
		previous[r.ID] = r
	}
	for _, r := range next.snapshot.Records {
		if p, ok := previous[r.ID]; ok {
			if p.OwnerID != r.OwnerID || p.Name != r.Name || p.Type != r.Type {
				return errors.New("DNS record ownership is immutable")
			}
			if r.Generation < p.Generation || r.Generation == p.Generation && !reflect.DeepEqual(p, r) {
				return errors.New("stale DNS record generation")
			}
		}
	}
	return nil
}
