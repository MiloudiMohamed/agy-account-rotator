package rotate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

func newEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	root := t.TempDir()
	s := &store.Store{Root: root, TokenPathOverride: filepath.Join(root, "live.json")}
	if err := os.MkdirAll(s.AccountsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConfig(store.Config{Mode: "round-robin"}); err != nil {
		t.Fatal(err)
	}
	return New(s), s
}

func addAccounts(t *testing.T, s *store.Store, emails ...string) {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	for i, e := range emails {
		a := &store.Account{
			Email:   e,
			AddedAt: base.Add(time.Duration(i) * time.Second),
			File:    store.TokenFile{AuthMethod: "consumer", Token: store.Token{RefreshToken: "rt-" + e}},
		}
		if err := s.SaveAccount(a); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPickNextRoundRobinCycles(t *testing.T) {
	e, _ := newEngine(t)
	addAccounts(t, e.S, "a@x", "b@x", "c@x")

	want := []string{"a@x", "b@x", "c@x", "a@x", "b@x"}
	for i, w := range want {
		a, err := e.PickNext()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Activate(a); err != nil { // Activate advances usage bookkeeping
			t.Fatal(err)
		}
		if a.Email != w {
			t.Fatalf("pick %d = %s, want %s", i, a.Email, w)
		}
	}
}

func TestPickNextSkipsCooling(t *testing.T) {
	e, _ := newEngine(t)
	addAccounts(t, e.S, "a@x", "b@x", "c@x")

	// cool down b for an hour
	if _, err := e.ReportFailure("b@x", "QUOTA_EXHAUSTED"); err != nil {
		t.Fatal(err)
	}

	seq := []string{"a@x", "c@x", "a@x"} // b must never appear
	for i, w := range seq {
		a, err := e.PickNext()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Activate(a); err != nil {
			t.Fatal(err)
		}
		if a.Email == "b@x" {
			t.Fatalf("pick %d returned cooling account b@x", i)
		}
		if a.Email != w {
			t.Fatalf("pick %d = %s, want %s", i, a.Email, w)
		}
	}
}

func TestAllCoolingReturnsSoonest(t *testing.T) {
	e, _ := newEngine(t)
	addAccounts(t, e.S, "a@x", "b@x")

	d1, _ := e.ReportFailure("a@x", "QUOTA_EXHAUSTED") // 60s
	d2, _ := e.ReportFailure("b@x", "QUOTA_EXHAUSTED") // 5m (2nd failure? no — first failure each)
	_ = d2
	if d1 != 60*time.Second {
		t.Fatalf("unexpected d1 %v", d1)
	}
	a, err := e.PickNext() // everything cooling → soonest expiry wins
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "a@x" {
		t.Fatalf("expected soonest-cooling a@x, got %s", a.Email)
	}
}

func TestStickyKeepsActiveWhileHealthy(t *testing.T) {
	e, s := newEngine(t)
	addAccounts(t, e.S, "a@x", "b@x")
	if err := s.SaveConfig(store.Config{Mode: "sticky"}); err != nil {
		t.Fatal(err)
	}

	a1, _ := e.PickNext()
	e.Activate(a1)
	for i := 0; i < 3; i++ {
		a, err := e.PickNext()
		if err != nil {
			t.Fatal(err)
		}
		if a.Email != a1.Email {
			t.Fatalf("sticky mode drifted to %s after %d picks", a.Email, i+1)
		}
	}

	// once active cools down, sticky moves on
	if _, err := e.ReportFailure(a1.Email, "QUOTA_EXHAUSTED"); err != nil {
		t.Fatal(err)
	}
	a, err := e.PickNext()
	if err != nil {
		t.Fatal(err)
	}
	if a.Email == a1.Email {
		t.Fatal("sticky mode kept cooling account")
	}
}

func TestActivateClearsFailuresAndSetsActive(t *testing.T) {
	e, s := newEngine(t)
	addAccounts(t, e.S, "a@x")

	e.ReportFailure("a@x", "RATE_LIMIT_EXCEEDED")
	e.ReportFailure("a@x", "QUOTA_EXHAUSTED")

	st := s.LoadState()
	if st.Accounts["a@x"].Failures != 2 {
		t.Fatalf("failures = %d, want 2", st.Accounts["a@x"].Failures)
	}

	a, _ := s.GetAccount("a@x")
	if err := e.Activate(a); err != nil {
		t.Fatal(err)
	}
	st = s.LoadState()
	if st.ActiveEmail != "a@x" {
		t.Fatalf("active = %q", st.ActiveEmail)
	}
	if st.Accounts["a@x"].Failures != 0 || !st.Accounts["a@x"].CoolUntil.IsZero() {
		t.Fatalf("activate did not clear failure state: %+v", st.Accounts["a@x"])
	}
	live, err := s.ReadTokenFile()
	if err != nil || live.Token.RefreshToken != "rt-a@x" {
		t.Fatalf("live token file not written: %+v err=%v", live, err)
	}
}

func TestNoAccounts(t *testing.T) {
	e, _ := newEngine(t)
	if _, err := e.PickNext(); err == nil {
		t.Fatal("expected error for empty vault")
	}
}

func TestPickNextSmartMode(t *testing.T) {
	e, s := newEngine(t)
	addAccounts(t, e.S, "low@x", "high@x")
	if err := s.SaveConfig(store.Config{Mode: "smart"}); err != nil {
		t.Fatal(err)
	}

	qc := s.LoadQuotaCache()
	qc.FetchedAt = time.Now().UTC()
	qc.Accounts["low@x"] = store.AccountQuotaCache{
		Groups: map[string]store.QuotaGroupCache{
			"claude": {RemainingFraction: 0.10},
		},
		CachedAt: time.Now().UTC(),
	}
	qc.Accounts["high@x"] = store.AccountQuotaCache{
		Groups: map[string]store.QuotaGroupCache{
			"claude": {RemainingFraction: 0.95},
		},
		CachedAt: time.Now().UTC(),
	}
	if err := s.SaveQuotaCache(qc); err != nil {
		t.Fatal(err)
	}

	picked, err := e.PickNext()
	if err != nil {
		t.Fatal(err)
	}
	if picked.Email != "high@x" {
		t.Fatalf("smart mode picked %s, want high@x", picked.Email)
	}
}

func TestAuthDeadRevocation(t *testing.T) {
	e, s := newEngine(t)
	addAccounts(t, e.S, "revoked@x", "healthy@x")

	// Mark revoked@x with AuthDead
	if _, err := e.ReportFailure("revoked@x", "AUTH_REVOKED"); err != nil {
		t.Fatal(err)
	}
	st := s.LoadState()
	if !st.Accounts["revoked@x"].Revoked {
		t.Fatal("expected revoked flag set")
	}

	// PickNext should pick healthy@x consistently
	for i := 0; i < 3; i++ {
		picked, err := e.PickNext()
		if err != nil {
			t.Fatal(err)
		}
		if picked.Email != "healthy@x" {
			t.Fatalf("expected healthy@x, got %s", picked.Email)
		}
	}

	// Clearing revocation via ReportSuccess
	if err := e.ReportSuccess("revoked@x"); err != nil {
		t.Fatal(err)
	}
	st = s.LoadState()
	if st.Accounts["revoked@x"].Revoked {
		t.Fatal("expected revoked flag cleared")
	}
}
