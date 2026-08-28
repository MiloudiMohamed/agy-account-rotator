// Package history maintains an append-only JSONL log of rotation events,
// cooldown transitions, and account changes for auditability and debugging.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

type EventType string

const (
	EventRotate     EventType = "rotate"
	EventActivate   EventType = "activate"
	EventFailure    EventType = "failure"
	EventCooldown   EventType = "cooldown"
	EventAdd        EventType = "add"
	EventRemove     EventType = "remove"
	EventRevalidate EventType = "revalidate"
)

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Type      EventType `json:"type"`
	Email     string    `json:"email"`
	Reason    string    `json:"reason,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

func historyPath(s *store.Store) string {
	return filepath.Join(s.Root, "history.jsonl")
}

// Append logs an event to history.jsonl.
func Append(s *store.Store, ev Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	p := historyPath(s)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Read returns up to limit most recent events matching emailFilter (if non-empty),
// ordered chronologically from oldest to newest.
func Read(s *store.Store, limit int, emailFilter string) ([]Event, error) {
	p := historyPath(s)
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var all []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if emailFilter != "" && ev.Email != emailFilter {
			continue
		}
		all = append(all, ev)
	}

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

// LastEvent returns the most recent event for an email (or across all if email is empty).
func LastEvent(s *store.Store, email string) (*Event, error) {
	events, err := Read(s, 0, email)
	if err != nil || len(events) == 0 {
		return nil, err
	}
	return &events[len(events)-1], nil
}

// Format returns a human-readable table line for an event.
func Format(ev Event) string {
	ts := ev.Timestamp.Local().Format("2006-01-02 15:04:05")
	tType := strings.ToUpper(string(ev.Type))
	extra := ""
	if ev.Reason != "" {
		extra = ev.Reason
	}
	if ev.Detail != "" {
		if extra != "" {
			extra += " — " + ev.Detail
		} else {
			extra = ev.Detail
		}
	}
	if extra != "" {
		return fmt.Sprintf("%s  %-10s  %-30s  %s", ts, tType, ev.Email, extra)
	}
	return fmt.Sprintf("%s  %-10s  %-30s", ts, tType, ev.Email)
}
