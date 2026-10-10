package uipreview

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const accessPath = "/__dispatch_ui/access"
const accessCookie = "__Secure-dispatch-ui-access"
const accessDuration = time.Hour

func (s *Server) accessMAC(payload string) []byte {
	mac := hmac.New(sha256.New, s.accessKey[:])
	_, _ = io.WriteString(mac, "dispatch-ui-preview-access-v1\n"+s.origin.String()+"\n"+s.production.String()+"\n"+payload)
	return mac.Sum(nil)
}

func (s *Server) newAccessCookie() (*http.Cookie, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	expires := s.now().Add(accessDuration)
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce[:])
	value := payload + "." + base64.RawURLEncoding.EncodeToString(s.accessMAC(payload))
	return &http.Cookie{Name: accessCookie, Value: value, Domain: s.origin.Hostname(), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(accessDuration.Seconds()), Expires: expires}, nil
}

func (s *Server) productionAccess(r *http.Request) (time.Time, bool) {
	cookie, err := r.Cookie(accessCookie)
	if err != nil || len(cookie.Value) > 160 {
		return time.Time{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, s.accessMAC(parts[0]+"."+parts[1])) {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	expires := time.Unix(seconds, 0)
	now := s.now()
	return expires, err == nil && expires.After(now) && !expires.After(now.Add(accessDuration))
}

func (s *Server) access(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		problem(w, http.StatusMethodNotAllowed, "Use POST to unlock production or DELETE to lock it.")
		return
	}
	if r.Header.Get("Origin") != s.origin.Scheme+"://"+r.Host {
		problem(w, http.StatusForbidden, "Origin required.")
		return
	}
	if r.Method == http.MethodDelete {
		http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: "", Domain: s.origin.Hostname(), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: sourceCookie, Value: "development", Domain: s.origin.Hostname(), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
	} else {
		var input struct {
			Key string `json:"key"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			problem(w, http.StatusBadRequest, "Enter the preview access key.")
			return
		}
		digest := sha256.Sum256([]byte(input.Key))
		if subtle.ConstantTimeCompare(digest[:], s.accessKey[:]) != 1 {
			problem(w, http.StatusForbidden, "The preview access key is incorrect.")
			return
		}
		cookie, err := s.newAccessCookie()
		if err != nil {
			problem(w, http.StatusInternalServerError, "Unable to unlock production. Try again.")
			return
		}
		http.SetCookie(w, cookie)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
