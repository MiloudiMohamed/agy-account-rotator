// Package stats aggregates local CLI usage, conversation volume, and
// per-account rotation metrics from agy's local data stores.
package stats

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

type StatsReport struct {
	TotalConversations int            `json:"total_conversations"`
	TotalSteps         int            `json:"total_steps"`
	TotalStorageBytes  int64          `json:"total_storage_bytes"`
	FirstActive        time.Time      `json:"first_active,omitempty"`
	LastActive         time.Time      `json:"last_active,omitempty"`
	ActiveToday        int            `json:"active_today"`
	ActiveThisWeek     int            `json:"active_this_week"`
	TotalRotations     int            `json:"total_rotations"`
	TotalCooldowns     int            `json:"total_cooldowns"`
	AccountSummaries   []AccountStats `json:"account_summaries"`
}

type AccountStats struct {
	Email    string    `json:"email"`
	Active   bool      `json:"active"`
	Status   string    `json:"status"`
	LastUsed time.Time `json:"last_used,omitempty"`
	Failures int       `json:"failures"`
	Switches int       `json:"switches"`
}

func Collect(s *store.Store) (*StatsReport, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")

	rep := &StatsReport{
		AccountSummaries: []AccountStats{},
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekStart := todayStart.AddDate(0, 0, -int(now.Weekday()))

	// Scan conversations directory
	if entries, err := os.ReadDir(convDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
				continue
			}
			rep.TotalConversations++
			p := filepath.Join(convDir, e.Name())
			if fi, err := os.Stat(p); err == nil {
				rep.TotalStorageBytes += fi.Size()
				mt := fi.ModTime()
				if rep.FirstActive.IsZero() || mt.Before(rep.FirstActive) {
					rep.FirstActive = mt
				}
				if rep.LastActive.IsZero() || mt.After(rep.LastActive) {
					rep.LastActive = mt
				}
				if mt.After(todayStart) {
					rep.ActiveToday++
				}
				if mt.After(weekStart) {
					rep.ActiveThisWeek++
				}
			}
		}
	}

	// Count total steps via sqlite3 CLI if available (quick aggregate)
	if rep.TotalConversations > 0 {
		rep.TotalSteps = countTotalSteps(convDir)
	}

	// Analyze history.jsonl
	switchCount := map[string]int{}
	if events, err := history.Read(s, 0, ""); err == nil {
		for _, ev := range events {
			switch ev.Type {
			case history.EventRotate:
				rep.TotalRotations++
				switchCount[ev.Email]++
			case history.EventCooldown, history.EventFailure:
				rep.TotalCooldowns++
			}
		}
	}

	// Aggregate vaulted accounts
	accounts, _ := s.ListAccounts()
	st := s.LoadState()
	for _, a := range accounts {
		as := st.Accounts[a.Email]
		status := "healthy"
		if as.Revoked {
			status = "revoked"
		} else if !as.CoolUntil.IsZero() && now.Before(as.CoolUntil) {
			status = "cooling"
		}
		rep.AccountSummaries = append(rep.AccountSummaries, AccountStats{
			Email:    a.Email,
			Active:   a.Email == st.ActiveEmail,
			Status:   status,
			LastUsed: as.LastUsed,
			Failures: as.Failures,
			Switches: switchCount[a.Email],
		})
	}

	return rep, nil
}

func countTotalSteps(dir string) int {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		out, err := exec.Command("sqlite3", p, "SELECT count(*) FROM steps;").Output()
		if err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil {
				total += n
			}
		}
	}
	return total
}

func FormatStorage(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(bytes)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "B"
}
