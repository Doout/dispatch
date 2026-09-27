package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	passwordWindow       = 15 * time.Minute
	passwordAccountLimit = 10
	passwordClientLimit  = 60
	publicWindow         = time.Minute
	publicClientLimit    = 120
)

func throttleKey(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}

// clientAddress accepts forwarding headers only from explicitly trusted network peers.
func (a *API) clientAddress(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil {
		return peer
	}
	if !a.trustedProxyIP(peerIP) {
		return peerIP.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(forwarded[i]))
		if ip == nil {
			return peerIP.String()
		}
		if !a.trustedProxyIP(ip) {
			return ip.String()
		}
	}
	return peerIP.String()
}

func (a *API) trustedProxyIP(ip net.IP) bool {
	for _, network := range a.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *API) trustedProxyPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return a.trustedProxyIP(net.ParseIP(host))
}

func (a *API) limitPublicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := throttleKey("public:"+r.URL.Path, a.clientAddress(r))
		count, err := a.store.RecordAuthAttempt(r.Context(), key, time.Now().UTC(), publicWindow)
		if err != nil {
			a.internal(w, err)
			return
		}
		if count > publicClientLimit {
			tooManyAuthAttempts(w, 60)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) passwordAllowed(w http.ResponseWriter, r *http.Request, username string) bool {
	now := time.Now().UTC()
	accountKey := throttleKey("password:account", username)
	clientKey := throttleKey("password:client", a.clientAddress(r))
	accountCount, err := a.store.RecordAuthAttempt(r.Context(), accountKey, now, passwordWindow)
	if err != nil {
		a.internal(w, err)
		return false
	}
	if accountCount > passwordAccountLimit {
		tooManyAuthAttempts(w, int(passwordWindow.Seconds()))
		return false
	}
	clientCount, err := a.store.RecordAuthAttempt(r.Context(), clientKey, now, passwordWindow)
	if err != nil {
		a.internal(w, err)
		return false
	}
	if clientCount > passwordClientLimit {
		tooManyAuthAttempts(w, int(passwordWindow.Seconds()))
		return false
	}
	return true
}

func (a *API) passwordSucceeded(w http.ResponseWriter, r *http.Request, username string) bool {
	if err := a.store.ClearAuthAttempts(r.Context(), throttleKey("password:account", username)); err != nil {
		a.internal(w, err)
		return false
	}
	if err := a.store.ReleaseAuthAttempt(r.Context(), throttleKey("password:client", a.clientAddress(r))); err != nil {
		a.internal(w, err)
		return false
	}
	return true
}

func tooManyAuthAttempts(w http.ResponseWriter, retrySeconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
	problem(w, http.StatusTooManyRequests, "Too many sign-in attempts", "Try again after the delay in Retry-After.")
}
