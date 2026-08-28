package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s := &Store{Root: root, TokenPathOverride: filepath.Join(root, "live-token.json")}
	for _, d := range []string{root, s.AccountsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func sampleAccount(email string, addedAt time.Time) *Account {
	return &Account{
		Email:   email,
		AddedAt: addedAt,
		File: TokenFile{
			Token: Token{
				AccessToken:  "at-" + email,
				TokenType:    "Bearer",
				RefreshToken: "rt-" + email,
				Expiry:       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
			AuthMethod: "consumer",
		},
	}
}

func TestAccountCRUDAndOrdering(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().UTC()

	mustSave(t, s, sampleAccount("c@example.com", base))
	// b ties with c on AddedAt; email breaks the tie (b < c)
	mustSave(t, s, sampleAccount("a@example.com", base.Add(-time.Minute)))
	mustSave(t, s, sampleAccount("b@example.com", base))

	got := accountList(s, t)
	want := []string{"a@example.com", "b@example.com", "c@example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %d accounts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d]=%s want %s (full=%v)", i, got[i], want[i], got)
		}
	}

	a, err := s.GetAccount("b@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a.File.Token.RefreshToken != "rt-b@example.com" {
		t.Fatalf("roundtrip lost refresh token: %q", a.File.Token.RefreshToken)
	}

	if err := s.RemoveAccount("ghost@example.com"); err != nil { // removing absent = no-op
		t.Fatal(err)
	}
	if err := s.RemoveAccount("a@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(accountList(s, t)) != 2 {
		t.Fatal("remove did not remove")
	}
}

func TestTokenFileRoundtripAndMode(t *testing.T) {
	s := newTestStore(t)

	tf := &TokenFile{AuthMethod: "consumer"}
	tf.Token.RefreshToken = "rt-live"

	if err := s.WriteTokenFile(tf); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.TokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	back, err := s.ReadTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	if back.AuthMethod != "consumer" || back.Token.RefreshToken != "rt-live" {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}

	// overwrite keeps atomicity and content
	tf.Token.AccessToken = "at-2"
	if err := s.WriteTokenFile(tf); err != nil {
		t.Fatal(err)
	}
	back, _ = s.ReadTokenFile()
	if back.Token.AccessToken != "at-2" {
		t.Fatal("overwrite failed")
	}
}

func TestConfigDefaults(t *testing.T) {
	s := newTestStore(t)
	cfg := s.LoadConfig()
	if got := cfg.Mode; got != "round-robin" {
		t.Fatalf("default mode = %q, want round-robin", got)
	}
	if !cfg.ProxyEnabled {
		t.Fatal("default ProxyEnabled should be true")
	}
	if cfg.ProxyPort != 8999 {
		t.Fatalf("default ProxyPort = %d, want 8999", cfg.ProxyPort)
	}
	if err := s.SaveConfig(Config{Mode: "banana"}); err != nil {
		t.Fatal(err)
	}
	if got := s.LoadConfig().Mode; got != "round-robin" {
		t.Fatalf("invalid mode not coerced: %q", got)
	}
}

func TestQuotaCache(t *testing.T) {
	s := newTestStore(t)
	qc := s.LoadQuotaCache()
	if len(qc.Accounts) != 0 {
		t.Fatalf("expected empty quota cache, got %d accounts", len(qc.Accounts))
	}

	qc.Accounts["test@gmail.com"] = AccountQuotaCache{
		Groups: map[string]QuotaGroupCache{
			"claude": {RemainingFraction: 0.8, ResetTime: "2026-08-26T12:00:00Z", ModelCount: 2},
		},
		CachedAt: time.Now().UTC(),
	}
	if err := s.SaveQuotaCache(qc); err != nil {
		t.Fatal(err)
	}

	reloaded := s.LoadQuotaCache()
	if len(reloaded.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(reloaded.Accounts))
	}
	if reloaded.Accounts["test@gmail.com"].Groups["claude"].RemainingFraction != 0.8 {
		t.Fatalf("mismatched cached quota fraction: %+v", reloaded.Accounts["test@gmail.com"])
	}
}

// helpers that fail the test on error

func mustSave(t *testing.T, s *Store, a *Account) {
	t.Helper()
	if err := s.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
}

func mustWriteToken(t *testing.T, s *Store, refresh string) {
	t.Helper()
	tf := &TokenFile{AuthMethod: "consumer", Token: Token{RefreshToken: refresh}}
	if err := s.WriteTokenFile(tf); err != nil {
		t.Fatal(err)
	}
}

func accountList(s *Store, t *testing.T) []string {
	t.Helper()
	accounts, err := s.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.Email
	}
	return out
}
