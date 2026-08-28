package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Auth endpoint and parameters — identical to what the Antigravity CLI itself
// uses (public installed-app client, PKCE S256).
const (
	authEndpoint = "https://accounts.google.com/o/oauth2/auth"

	redirectURI = "https://antigravity.google/oauth-callback"
)

var authScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
	"openid",
}

// NewPKCE returns a fresh code_verifier and its S256 code_challenge.
func NewPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 64)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// RandomState returns an opaque state parameter.
func RandomState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// AuthCodeURL builds the Google sign-in URL for agy's OAuth client.
func AuthCodeURL(state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(authScopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return authEndpoint + "?" + q.Encode()
}

// Exchange swaps an authorization code (plus our PKCE verifier) for tokens.
func Exchange(ctx context.Context, httpClient *http.Client, code, verifier string) (*RefreshResponse, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		RefreshResponse
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("token endpoint: bad json (status %d): %w", resp.StatusCode, err)
	}
	if body.Error != "" {
		detail := body.ErrorDescription
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("%s: %s", body.Error, detail)
	}
	if body.AccessToken == "" || body.RefreshToken == "" {
		return nil, fmt.Errorf("token endpoint: missing token fields (status %d)", resp.StatusCode)
	}
	return &body.RefreshResponse, nil
}

// ParsePasted extracts the authorization code from either a full redirect URL
// or a bare code. Accepts common paste noise: surrounding whitespace/quotes,
// HTML-escaped slashes (%2F is handled by url parsing; "4/0..." literal too).
func ParsePasted(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	if s == "" {
		return "", fmt.Errorf("nothing pasted")
	}
	if !strings.Contains(s, "://") && !strings.Contains(s, " ") {
		return s, nil // bare code like "4/0AbC..."
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("could not parse pasted text as URL: %w", err)
	}
	code := u.Query().Get("code")
	if code == "" {
		return "", fmt.Errorf("no 'code=' parameter found in pasted URL")
	}
	return code, nil
}

// RedirectHost is where Google lands the browser after approval. The page
// does not load (no server) — users copy it from the address bar.
func RedirectHost() string { return redirectURI }
