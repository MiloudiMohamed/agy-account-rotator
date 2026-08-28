package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshOK(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotForm = r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at-new","expires_in":3599,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	rr, err := Refresh(context.Background(), srv.Client(), "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotForm != "refresh_token" {
		t.Fatalf("grant_type = %q", gotForm)
	}
	if rr.AccessToken != "at-new" || rr.ExpiresIn != 3599 {
		t.Fatalf("bad refresh response: %+v", rr)
	}
	if rr.RefreshToken != "" {
		t.Fatalf("rotation field should be absent when not rotated: %q", rr.RefreshToken)
	}
}

func TestRefreshError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	_, err := Refresh(context.Background(), srv.Client(), "rt-dead")
	if err == nil {
		t.Fatal("expected error")
	}
	if !containsAll(err.Error(), "invalid_grant", "expired or revoked") {
		t.Fatalf("error lacks detail: %v", err)
	}
}

func TestFetchEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"email":"user@example.com","email_verified":true}`))
	}))
	defer srv.Close()

	old := userinfoEndpoint
	userinfoEndpoint = srv.URL
	defer func() { userinfoEndpoint = old }()

	email, err := FetchEmail(context.Background(), srv.Client(), "at-x")
	if err != nil {
		t.Fatal(err)
	}
	if email != "user@example.com" {
		t.Fatalf("email = %q", email)
	}
	if _, err := FetchEmail(context.Background(), srv.Client(), "wrong"); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestExpiryFrom(t *testing.T) {
	now := time.Now()
	got := ExpiryFrom(now, 60)
	if diff := got.Sub(now.UTC()); diff < 59*time.Second || diff > 61*time.Second {
		t.Fatalf("expiry delta = %v", diff)
	}
	fallback := ExpiryFrom(now, 0)
	if fallback.Before(now.Add(time.Hour).Add(-time.Minute)) {
		t.Fatal("zero expires_in should fall back to ~1h")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
