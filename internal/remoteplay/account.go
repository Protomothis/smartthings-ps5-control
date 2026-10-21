package remoteplay

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The PSN OAuth client of the Windows Remote Play app. These are public
// values shipped in that app (and in chiaki, pyremoteplay, playactor); the
// flow only reads the account's numeric ID.
const (
	oauthClientID     = "ba495a24-818c-472b-b12d-ff231c1b5745"
	oauthClientSecret = "mvaiZkRsAsI1IBkY" //nolint:gosec // public client secret of the Remote Play app, not ours
	oauthRedirectURI  = "https://remoteplay.dl.playstation.net/remoteplay/redirect"
)

// LoginURL is the PSN sign-in page. After signing in, the browser lands on
// a remoteplay.dl.playstation.net redirect URL whose code parameter
// AccountIDFromRedirectURL turns into the account ID.
const LoginURL = "https://auth.api.sonyentertainmentnetwork.com/2.0/oauth/authorize" +
	"?service_entity=urn:service-entity:psn&response_type=code&client_id=" + oauthClientID +
	"&redirect_uri=" + oauthRedirectURI +
	"&scope=psn:clientapp&request_locale=en_US&ui=pr&service_logo=ps&layout_type=popup" +
	"&smcid=remoteplay&prompt=always&PlatformPrivacyWs1=minimal&"

// tokenURL and oauthHTTP are variables so tests can point them at a fake.
var (
	tokenURL  = "https://auth.api.sonyentertainmentnetwork.com/2.0/oauth/token" //nolint:gosec // an endpoint URL, not a credential
	oauthHTTP = &http.Client{Timeout: 20 * time.Second}
)

// AccountIDFromUserID turns the decimal PSN user_id into the account ID the
// registration wants: the number as 8 little-endian bytes, base64.
func AccountIDFromUserID(userID string) (string, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(userID), 10, 64)
	if err != nil {
		return "", fmt.Errorf("remoteplay: user ID %q is not a number", userID)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// AccountIDFromRedirectURL finishes the PSN sign-in started at LoginURL:
// it exchanges the code in redirectURL for an access token and looks up the
// account the token belongs to.
func AccountIDFromRedirectURL(ctx context.Context, redirectURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(redirectURL))
	if err != nil {
		return "", fmt.Errorf("remoteplay: redirect URL: %w", err)
	}
	code := u.Query().Get("code")
	if code == "" {
		return "", errors.New("remoteplay: redirect URL has no code parameter; copy the whole address after signing in")
	}

	form := url.Values{
		"code":         {code},
		"grant_type":   {"authorization_code"},
		"redirect_uri": {oauthRedirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := oauthJSON(req, "token exchange", &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", errors.New("remoteplay: token exchange answered without access_token")
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, tokenURL+"/"+url.PathEscape(tok.AccessToken), nil)
	if err != nil {
		return "", err
	}
	var info struct {
		UserID json.RawMessage `json:"user_id"`
	}
	if err := oauthJSON(req, "account lookup", &info); err != nil {
		return "", err
	}
	// user_id is a decimal string; accept a bare number too.
	return AccountIDFromUserID(strings.Trim(string(info.UserID), `"`))
}

func oauthJSON(req *http.Request, step string, out any) error {
	req.SetBasicAuth(oauthClientID, oauthClientSecret)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("remoteplay: %s: %w", step, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("remoteplay: %s: %w", step, err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		msg := fmt.Sprintf("remoteplay: %s: HTTP %d", step, resp.StatusCode)
		if e.Error != "" {
			msg += " (" + e.Error
			if e.Description != "" {
				msg += ": " + e.Description
			}
			msg += ")"
		}
		return errors.New(msg)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("remoteplay: %s: %w", step, err)
	}
	return nil
}
