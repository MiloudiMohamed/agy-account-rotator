// Package google implements the minimal Google OAuth operations the rotator
// needs: refreshing an account's access token and resolving its email.
//
// Credentials are agy's public installed-app OAuth client (same values used by
// opencode-antigravity-auth / gemini-cli OSS).
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
)

// Endpoints are vars so tests can point them at a stub server.
var (
	tokenEndpoint    = "https://oauth2.googleapis.com/token"
	userinfoEndpoint = "https://www.googleapis.com/oauth2/v3/userinfo"
)

// RefreshResponse is the subset of the token endpoint response we consume.
type RefreshResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"` // present only if rotated
}

// Refresh exchanges a refresh token for a fresh access token.
func Refresh(ctx context.Context, httpClient *http.Client, refreshToken string) (*RefreshResponse, error) {
	form := url.Values{
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
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
		return nil, fmt.Errorf("token endpoint: %s: %s", body.Error, detail)
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint: empty access token (status %d)", resp.StatusCode)
	}
	return &body.RefreshResponse, nil
}

// FetchEmail resolves the account email for an access token.
func FetchEmail(ctx context.Context, httpClient *http.Client, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoEndpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo: %s", resp.Status)
	}
	var ui struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		return "", err
	}
	if ui.Email == "" {
		return "", fmt.Errorf("userinfo: no email in response")
	}
	return ui.Email, nil
}

// ExpiryFrom computes a token expiry timestamp from now + expires_in.
func ExpiryFrom(now time.Time, expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return now.Add(time.Duration(expiresIn) * time.Second).UTC()
}
