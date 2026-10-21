package remoteplay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePSN stands in for the PSN token endpoint: POST exchanges the code,
// GET /<token> describes the account.
func fakePSN(t *testing.T, userID any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := r.BasicAuth(); !ok || id != oauthClientID || secret != oauthClientSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil ||
				r.PostForm.Get("code") != "abc123" ||
				r.PostForm.Get("grant_type") != "authorization_code" ||
				r.PostForm.Get("redirect_uri") != oauthRedirectURI {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "bad code"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "token_type": "bearer"})
		case r.Method == http.MethodGet && r.URL.Path == "/token/tok-1":
			json.NewEncoder(w).Encode(map[string]any{"user_id": userID, "online_id": "someone"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	oldURL, oldHTTP := tokenURL, oauthHTTP
	tokenURL, oauthHTTP = srv.URL+"/token", srv.Client()
	t.Cleanup(func() { tokenURL, oauthHTTP = oldURL, oldHTTP })
}

func TestAccountIDFromRedirectURL(t *testing.T) {
	const redirect = "https://remoteplay.dl.playstation.net/remoteplay/redirect?code=abc123&cid=x"
	for _, userID := range []any{"1234567890123", 1234567890123} {
		fakePSN(t, userID)
		got, err := AccountIDFromRedirectURL(context.Background(), redirect)
		if err != nil || got != "ywT7cR8BAAA=" {
			t.Errorf("user_id %v: got %q, %v; want ywT7cR8BAAA=", userID, got, err)
		}
	}
}

func TestAccountIDFromRedirectURLErrors(t *testing.T) {
	fakePSN(t, "1234567890123")
	for _, tc := range []struct{ url, want string }{
		{"https://remoteplay.dl.playstation.net/remoteplay/redirect?error=access_denied", "no code"},
		{"https://remoteplay.dl.playstation.net/remoteplay/redirect?code=wrong", "invalid_grant: bad code"},
	} {
		_, err := AccountIDFromRedirectURL(context.Background(), tc.url)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to mention %q", tc.url, err, tc.want)
		}
	}
}
