package hosted

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginBucket struct {
	start time.Time
	count int
}
type loginThrottle struct {
	sync.Mutex
	entries map[string]loginBucket
}

func (t *loginThrottle) allow(r *http.Request, email string) bool {
	t.Lock()
	defer t.Unlock()
	now := time.Now()
	if t.entries == nil {
		t.entries = map[string]loginBucket{}
	}
	for key, b := range t.entries {
		if now.Sub(b.start) > 5*time.Minute {
			delete(t.entries, key)
		}
	}
	if len(t.entries) > 10000 {
		return false
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	keys := []string{"ip:" + ip, "email:" + fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)))))}
	allowed := true
	for _, key := range keys {
		b := t.entries[key]
		if b.start.IsZero() {
			b.start = now
		}
		b.count++
		t.entries[key] = b
		limit := 10
		if strings.HasPrefix(key, "ip:") {
			limit = 100
		}
		if b.count > limit {
			allowed = false
		}
	}
	return allowed
}
