package statusline

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	return &store.Store{Root: root, TokenPathOverride: filepath.Join(root, "token.json")}
}

func TestRenderSegment(t *testing.T) {
	s := newTestStore(t)

	// Empty store
	if got := RenderSegment(s, true); got != "" {
		t.Fatalf("expected empty segment for empty store, got %q", got)
	}

	// Active account set without cached quota
	st := s.LoadState()
	st.ActiveEmail = "developer@example.com"
	s.SaveState(st)

	got := RenderSegment(s, true)
	if got != "⬤ developer" {
		t.Fatalf("no-quota segment = %q, want '⬤ developer'", got)
	}

	// With quota cache
	qc := s.LoadQuotaCache()
	qc.FetchedAt = time.Now().UTC()
	qc.Accounts["developer@example.com"] = store.AccountQuotaCache{
		Groups: map[string]store.QuotaGroupCache{
			"claude":       {RemainingFraction: 1.0},
			"gemini-pro":   {RemainingFraction: 0.73},
			"gemini-flash": {RemainingFraction: 0.15},
		},
		CachedAt: time.Now().UTC(),
	}
	s.SaveQuotaCache(qc)

	gotNoColor := RenderSegment(s, true)
	if !strings.Contains(gotNoColor, "⬤ developer") ||
		!strings.Contains(gotNoColor, "C:100%") ||
		!strings.Contains(gotNoColor, "P:73%") ||
		!strings.Contains(gotNoColor, "F:15%") {
		t.Fatalf("unexpected no-color segment: %q", gotNoColor)
	}

	// Colored segment must contain ANSI escape codes
	gotColor := RenderSegment(s, false)
	if !strings.Contains(gotColor, "\033[") {
		t.Fatalf("expected ANSI color codes in %q", gotColor)
	}

	// Cooling account
	st.Accounts["developer@example.com"] = store.AccountState{
		CoolUntil: time.Now().Add(5 * time.Minute),
	}
	s.SaveState(st)

	gotCooling := RenderSegment(s, true)
	if !strings.Contains(gotCooling, "❄ developer") {
		t.Fatalf("expected cooling snowflake icon in %q", gotCooling)
	}
}

func TestPayload(t *testing.T) {
	s := newTestStore(t)
	st := s.LoadState()
	st.ActiveEmail = "alice@gmail.com"
	s.SaveState(st)

	p := Payload(s)
	if p.ActiveEmail != "alice@gmail.com" || p.Username != "alice" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}
