package history

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

func TestHistoryAppendAndRead(t *testing.T) {
	s := newTestStore(t)

	// Read on non-existent history
	evs, err := Read(s, 10, "")
	if err != nil || len(evs) != 0 {
		t.Fatalf("expected empty events without error, got len=%d err=%v", len(evs), err)
	}

	t0 := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	if err := Append(s, Event{Timestamp: t0, Type: EventAdd, Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(s, Event{Timestamp: t0.Add(time.Minute), Type: EventRotate, Email: "b@x.com", Reason: "switched from a@x.com"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(s, Event{Timestamp: t0.Add(2 * time.Minute), Type: EventCooldown, Email: "a@x.com", Reason: "QUOTA_EXHAUSTED", Detail: "cooling 60s"}); err != nil {
		t.Fatal(err)
	}

	all, err := Read(s, 0, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("expected 3 events, got %d (err: %v)", len(all), err)
	}

	// Filter by email
	filtered, err := Read(s, 0, "a@x.com")
	if err != nil || len(filtered) != 2 {
		t.Fatalf("expected 2 events for a@x.com, got %d", len(filtered))
	}

	// Limit test
	limited, err := Read(s, 2, "")
	if err != nil || len(limited) != 2 {
		t.Fatalf("expected 2 limited events, got %d", len(limited))
	}
	if limited[0].Email != "b@x.com" || limited[1].Email != "a@x.com" {
		t.Fatalf("unexpected limited slice: %+v", limited)
	}

	// LastEvent
	last, err := LastEvent(s, "")
	if err != nil || last == nil || last.Type != EventCooldown {
		t.Fatalf("expected EventCooldown last event, got %+v", last)
	}

	// Format
	formatted := Format(*last)
	if !strings.Contains(formatted, "COOLDOWN") || !strings.Contains(formatted, "QUOTA_EXHAUSTED") {
		t.Fatalf("format missing key parts: %q", formatted)
	}
}
