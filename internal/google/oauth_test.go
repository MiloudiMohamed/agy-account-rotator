package google

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// RFC 7636 appendix B vector — proves our S256 challenge derivation.
func TestPKCES256Vector(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	if got != challenge {
		t.Fatalf("challenge = %q, want %q", got, challenge)
	}
}

func TestNewPKCEGeneratesUsablePair(t *testing.T) {
	v1, c1, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	v2, c2, _ := NewPKCE()
	if v1 == v2 || c1 == c2 {
		t.Fatal("PKCE values must be random per call")
	}
	if strings.ContainsAny(v1, "+/=") {
		t.Fatalf("verifier must be base64url without padding: %q", v1)
	}
	sum := sha256.Sum256([]byte(v1))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != c1 {
		t.Fatal("challenge does not match verifier")
	}
}

func TestAuthCodeURL(t *testing.T) {
	u, err := url.Parse(AuthCodeURL("state-xyz", "challenge-abc"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id":             ClientID,
		"redirect_uri":          redirectURI,
		"response_type":         "code",
		"access_type":           "offline",
		"prompt":                "consent",
		"code_challenge":        "challenge-abc",
		"code_challenge_method": "S256",
		"state":                 "state-xyz",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, want := range authScopes {
		found := false
		for _, s := range scopes {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("scope %q missing from URL", want)
		}
	}
}

func TestParsePasted(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"4/0ATsMZqAVJ3vPLUxfkMTXZtv7xIixXhkdwyrKUKSx-K3PcT2iNw", "4/0ATsMZqAVJ3vPLUxfkMTXZtv7xIixXhkdwyrKUKSx-K3PcT2iNw", false},
		{"  https://antigravity.google/oauth-callback?code=4%2F0AbC&state=x  ", "4/0AbC", false},
		{"\"https://antigravity.google/oauth-callback?code=4/0AbC\"", "4/0AbC", false},
		{"https://antigravity.google/oauth-callback?state=x", "", true},
		{"   ", "", true},
		{"/quit", "/quit", false}, // bare token: passed through, exchange will reject it
	}
	for _, c := range cases {
		got, err := ParsePasted(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParsePasted(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePasted(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePasted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExchangeOK(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	tok, err := Exchange(context.Background(), srv.Client(), "4/0Code", "verifier-x")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || tok.ExpiresIn != 3600 {
		t.Fatalf("bad tokens: %+v", tok)
	}
	want := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"4/0Code"},
		"code_verifier": {"verifier-x"},
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"redirect_uri":  {redirectURI},
	}
	for k, v := range want {
		if form.Get(k) != v[0] {
			t.Errorf("form[%s] = %q, want %q", k, form.Get(k), v[0])
		}
	}
}

func TestExchangeErrorSurfacesDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"Missing code verifier."}`))
	}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	_, err := Exchange(context.Background(), srv.Client(), "c", "v")
	if err == nil || !strings.Contains(err.Error(), "Missing code verifier") {
		t.Fatalf("expected verifier error, got: %v", err)
	}
}
