package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/rotate"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	s := &store.Store{Root: root, TokenPathOverride: filepath.Join(root, "token.json")}
	if err := os.MkdirAll(s.AccountsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return s
}

func addAccount(t *testing.T, s *store.Store, email string) {
	t.Helper()
	a := &store.Account{
		Email:   email,
		AddedAt: time.Now().UTC(),
		File: store.TokenFile{
			Token: store.Token{
				AccessToken:  "at-" + email,
				RefreshToken: "rt-" + email,
				Expiry:       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
			AuthMethod: "consumer",
		},
	}
	if err := s.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
}

func TestCertManager(t *testing.T) {
	s := newTestStore(t)
	cm, err := LoadOrCreateCA(s)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.CAPath()); err != nil {
		t.Fatalf("CA file not created: %v", err)
	}
	if _, err := os.Stat(s.CAKeyPath()); err != nil {
		t.Fatalf("CA key file not created: %v", err)
	}

	// Dynamic minting
	leaf, err := cm.GetCertificate("cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatal(err)
	}
	if leaf == nil || len(leaf.Certificate) == 0 {
		t.Fatal("empty leaf certificate")
	}

	// Cache test
	leaf2, err := cm.GetCertificate("cloudcode-pa.googleapis.com")
	if err != nil || leaf != leaf2 {
		t.Fatal("certificate was not cached")
	}
}

func TestProxyHealthz(t *testing.T) {
	s := newTestStore(t)
	addAccount(t, s, "user1@gmail.com")
	eng := rotate.New(s)
	a, _ := s.GetAccount("user1@gmail.com")
	_ = eng.Activate(a)

	cm, err := LoadOrCreateCA(s)
	if err != nil {
		t.Fatal(err)
	}

	// Find random free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	srv := NewServer(s, eng, cm, port, 5*time.Minute)
	go func() {
		_ = srv.Start()
	}()
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()

	// Wait for server ready
	deadline := time.Now().Add(500 * time.Millisecond)
	var ready bool
	for time.Now().Before(deadline) {
		if IsRunning(port) {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("proxy failed to start in time")
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var hr HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatal(err)
	}
	if hr.Status != "ok" || hr.ActiveEmail != "user1@gmail.com" {
		t.Fatalf("unexpected health response: %+v", hr)
	}
}

func TestProxy429InFlightRetry(t *testing.T) {
	s := newTestStore(t)
	addAccount(t, s, "account1@gmail.com")
	addAccount(t, s, "account2@gmail.com")
	eng := rotate.New(s)
	a1, _ := s.GetAccount("account1@gmail.com")
	_ = eng.Activate(a1)

	var requestCount int64
	var authTokens []string

	// Mock upstream Google endpoint
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnt := atomic.AddInt64(&requestCount, 1)
		token := r.Header.Get("Authorization")
		authTokens = append(authTokens, token)

		if cnt == 1 {
			// First request on account 1 hits 429
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": {"code": 429, "message": "Resource has been exhausted (e.g. check quota)."}}`))
			return
		}

		// Second request (retried on account 2) succeeds
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "success"}]}}]}`))
	}))
	defer upstream.Close()

	cm, err := LoadOrCreateCA(s)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	proxySrv := NewServer(s, eng, cm, proxyPort, 5*time.Minute)
	go func() {
		_ = proxySrv.Start()
	}()
	defer func() {
		_ = proxySrv.Shutdown(context.Background())
	}()

	// Wait for proxy ready
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if IsRunning(proxyPort) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Create client routing via our proxy
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(cm.caCert)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	// Send request through proxy to upstream server
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1internal/models", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer at-account1@gmail.com")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// User client should receive HTTP 200 cleanly without ever seeing the 429!
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after in-flight retry, got %d (body: %s)", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "success") {
		t.Fatalf("unexpected body: %s", string(body))
	}

	// Check that active account was rotated to account2@gmail.com
	st := s.LoadState()
	if st.ActiveEmail != "account2@gmail.com" {
		t.Fatalf("expected active account to be account2@gmail.com, got %s", st.ActiveEmail)
	}
}

func TestProxySpawnHelpers(t *testing.T) {
	s := newTestStore(t)
	if IsRunning(64321) {
		t.Fatal("expected IsRunning to be false for unused port")
	}

	env := BinaryEnv(s)
	if len(env) == 0 {
		t.Fatal("expected BinaryEnv to return proxy environment variables")
	}

	hasHTTPSProxy := false
	for _, e := range env {
		if strings.HasPrefix(e, "HTTPS_PROXY=") {
			hasHTTPSProxy = true
		}
	}
	if !hasHTTPSProxy {
		t.Fatalf("BinaryEnv missing HTTPS_PROXY: %v", env)
	}
}

