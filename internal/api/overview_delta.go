package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type overviewSnapshot struct {
	version, scope string
	data           []byte
	expires        time.Time
}
type overviewCache struct {
	sync.Mutex
	entries []overviewSnapshot
}

func overviewScope(r *http.Request) string {
	sum := sha256.Sum256([]byte(r.Header.Get("Authorization") + "\x00" + r.Header.Get("Impersonate-User")))
	return hex.EncodeToString(sum[:])
}
func (c *overviewCache) remember(scope string, data []byte) string {
	sum := sha256.Sum256(append([]byte(scope), data...))
	version := hex.EncodeToString(sum[:])
	c.Lock()
	defer c.Unlock()
	entries := make([]overviewSnapshot, 0, len(c.entries)+1)
	size := len(data)
	for _, entry := range c.entries {
		if entry.version != version && time.Now().Before(entry.expires) {
			entries = append(entries, entry)
			size += len(entry.data)
		}
	}
	entries = append(entries, overviewSnapshot{version, scope, data, time.Now().Add(10 * time.Minute)})
	for len(entries) > 1 && (len(entries) > 64 || size > 64<<20) {
		size -= len(entries[0].data)
		entries = entries[1:]
	}
	c.entries = entries
	return version
}
func (c *overviewCache) find(scope, version string) ([]byte, bool) {
	c.Lock()
	defer c.Unlock()
	for _, entry := range c.entries {
		if entry.scope == scope && entry.version == version && time.Now().Before(entry.expires) {
			return entry.data, true
		}
	}
	return nil, false
}

type overviewPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
}

// Values use RawMessage so null, false and zero are preserved with omitempty.
func patchValue(v any) any { data, _ := json.Marshal(v); return json.RawMessage(data) }
func pointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
func diffOverview(before, after any, path string, ops *[]overviewPatch) {
	if reflect.DeepEqual(before, after) {
		return
	}
	old, oldOK := before.(map[string]any)
	next, nextOK := after.(map[string]any)
	if oldOK && nextOK {
		keys := make([]string, 0, len(old)+len(next))
		seen := map[string]bool{}
		for key := range old {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range next {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			a, aOK := old[key]
			b, bOK := next[key]
			p := pointer(path, key)
			switch {
			case !bOK:
				*ops = append(*ops, overviewPatch{Op: "remove", Path: p})
			case !aOK:
				*ops = append(*ops, overviewPatch{Op: "add", Path: p, Value: patchValue(b)})
			default:
				diffOverview(a, b, p, ops)
			}
		}
		return
	}
	a, aOK := before.([]any)
	b, bOK := after.([]any)
	if aOK && bOK {
		work := append([]any(nil), a...)
		// Align records by ID before comparing fields, so inserts and moves do not
		// resend unchanged records later in the list.
		keyed := uniqueIDs(a) && uniqueIDs(b)
		if keyed {
			remaining := make(map[string]bool, len(b))
			for _, item := range b {
				remaining[recordID(item)] = true
			}
			for i := len(work) - 1; i >= 0; i-- {
				if !remaining[recordID(work[i])] {
					*ops = append(*ops, overviewPatch{Op: "remove", Path: pointer(path, strconv.Itoa(i))})
					work = append(work[:i], work[i+1:]...)
				}
			}
		}
		for i, value := range b {
			p := pointer(path, strconv.Itoa(i))
			if keyed {
				found := -1
				for j := i; j < len(work); j++ {
					if recordID(work[j]) == recordID(value) {
						found = j
						break
					}
				}
				if found < 0 {
					*ops = append(*ops, overviewPatch{Op: "add", Path: p, Value: patchValue(value)})
					work = append(work, nil)
					copy(work[i+1:], work[i:])
					work[i] = value
					continue
				}
				if found != i {
					*ops = append(*ops, overviewPatch{Op: "move", Path: p, From: pointer(path, strconv.Itoa(found))})
					item := work[found]
					copy(work[i+1:found+1], work[i:found])
					work[i] = item
				}
			}
			if i >= len(work) {
				*ops = append(*ops, overviewPatch{Op: "add", Path: p, Value: patchValue(value)})
				work = append(work, value)
			} else {
				diffOverview(work[i], value, p, ops)
			}
		}
		for i := len(work) - 1; i >= len(b); i-- {
			*ops = append(*ops, overviewPatch{Op: "remove", Path: pointer(path, strconv.Itoa(i))})
		}
		return
	}
	*ops = append(*ops, overviewPatch{Op: "replace", Path: path, Value: patchValue(after)})
}
func recordID(value any) string {
	if item, ok := value.(map[string]any); ok {
		if id, ok := item["id"].(string); ok {
			return id
		}
	}
	return ""
}
func uniqueIDs(items []any) bool {
	seen := map[string]bool{}
	for _, item := range items {
		id := recordID(item)
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
