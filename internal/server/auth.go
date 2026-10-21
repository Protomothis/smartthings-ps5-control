package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
)

// Auth is HTTP basic auth for the settings page and config API.
// With an empty user the protected routes answer 503 instead of opening up.
type Auth struct {
	User string
	Pass string
}

// Enabled reports whether credentials were configured.
func (a Auth) Enabled() bool { return a.User != "" && a.Pass != "" }

func (a Auth) check(user, pass string) bool {
	hu, hp := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	wu, wp := sha256.Sum256([]byte(a.User)), sha256.Sum256([]byte(a.Pass))
	return subtle.ConstantTimeCompare(hu[:], wu[:]) == 1 && subtle.ConstantTimeCompare(hp[:], wp[:]) == 1
}

// Wrap guards next with basic auth.
func (a Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() {
			writeError(w, http.StatusServiceUnavailable,
				errors.New("settings are disabled: set PS5_AUTH_USER and PS5_AUTH_PASS"))
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || !a.check(user, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ps5-control", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
