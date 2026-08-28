package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	s := &store.Store{Root: root, TokenPathOverride: filepath.Join(root, "token.json")}
	_ = os.MkdirAll(s.AccountsDir(), 0o700)
	return s
}

func TestFormatStorage(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
	}
	for _, c := range cases {
		if got := FormatStorage(c.in); got != c.want {
			t.Errorf("FormatStorage(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCollectWithEmptyVault(t *testing.T) {
	s := newTestStore(t)
	rep, err := Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalRotations != 0 || len(rep.AccountSummaries) != 0 {
		t.Fatalf("unexpected report for empty vault: %+v", rep)
	}
}

func TestCollectWithAccounts(t *testing.T) {
	s := newTestStore(t)
	a := &store.Account{
		Email:   "user@example.com",
		AddedAt: time.Now().UTC(),
	}
	s.SaveAccount(a)
	st := s.LoadState()
	st.ActiveEmail = "user@example.com"
	s.SaveState(st)

	rep, err := Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.AccountSummaries) != 1 || rep.AccountSummaries[0].Email != "user@example.com" {
		t.Fatalf("expected 1 account summary, got %+v", rep.AccountSummaries)
	}
	if !rep.AccountSummaries[0].Active {
		t.Fatal("expected account to be active")
	}
}
