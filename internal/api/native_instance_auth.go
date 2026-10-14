package api

import (
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/instanceconnection"
)

func (a *API) nativeInstanceRequest(r *http.Request) bool {
	return a.eventConfig.InstanceConnection != nil && a.eventConfig.InstanceConnection.NativeRequest(r)
}

func (a *API) nativeInstanceIdentity(r *http.Request) (core.Identity, bool) {
	if !a.nativeInstanceRequest(r) {
		return core.Identity{}, false
	}
	cookie, err := r.Cookie(instancebridge.NativeSessionCookie)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 512 {
		return core.Identity{}, false
	}
	now := time.Now().UTC()
	user, err := a.store.SessionUser(r.Context(), sessionHash(cookie.Value), now)
	if err == nil && user.State == core.UserStateActive {
		return identityForUser(user), true
	}
	valid, err := a.store.AdminSessionValid(r.Context(), sessionHash(cookie.Value), now)
	return controllerIdentity("administrator"), err == nil && valid
}

func (a *API) respondLoginSession(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Cache-Control", "no-store")
	if instanceconnection.IsRemote(r) && !a.nativeInstanceRequest(r) {
		problem(w, http.StatusUnauthorized, "Sign-in expired", "Start sign-in again.")
		return
	}
	if a.nativeInstanceRequest(r) {
		http.SetCookie(w, &http.Cookie{Name: instancebridge.NativeSessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 12 * 60 * 60})
		// The browser uses the host-bound cookie. It never receives a reusable
		// self-hosted bearer token in the response body.
		writeJSON(w, http.StatusOK, map[string]string{"token": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) instanceOrAuthorizedLogout(w http.ResponseWriter, r *http.Request) {
	if a.nativeInstanceRequest(r) {
		a.logout(w, r)
		return
	}
	a.authorize(http.HandlerFunc(a.logout)).ServeHTTP(w, r)
}
