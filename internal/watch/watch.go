// Package watch tails agy's CLI log for quota/rate-limit failures and rotates
// accounts. It also periodically checks live quota to preemptively switch
// accounts before hitting a mid-session wall.
package watch

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/cloudcode"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/detect"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/google"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/notify"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/rotate"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// Run blocks until ctx is cancelled, tailing agy logs and polling quota.
func Run(ctx context.Context, s *store.Store, eng *rotate.Engine, quiet bool) error {
	logDir := store.LogDir()
	var curPath string
	var rdr *bufio.Reader
	var f *os.File
	var lastSize int64

	cfg := s.LoadConfig()

	// Periodic quota poller for proactive smart rotation
	go runQuotaPoller(ctx, s, eng, quiet)

	scan := func(line string) {
		reason := detect.Classify(line)
		if reason == detect.None {
			return
		}
		st := s.LoadState()
		active := st.ActiveEmail
		if active == "" {
			return
		}
		d, err := eng.ReportFailure(active, reason)
		if err != nil {
			return
		}

		msg := fmt.Sprintf("[%s] %s on %s — cooling %s",
			time.Now().Format("15:04:05"), reason, active, d.Truncate(time.Second))
		fmt.Println(msg)

		if cfg.Notifications {
			notify.Send("agy-rotator", fmt.Sprintf("%s on %s\nCooling for %s", reason, active, d.Truncate(time.Second)))
		}

		if !quiet {
			if next, err := eng.PickNext(); err == nil && next.Email != active {
				if err := eng.Activate(next); err == nil {
					switchMsg := fmt.Sprintf("        switched live credentials to %s (applies to next agy launch)", next.Email)
					fmt.Println(switchMsg)
					if cfg.Notifications {
						notify.Send("agy-rotator", fmt.Sprintf("Switched live account to %s", next.Email))
					}
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		newest := newestLog(logDir)
		if newest == "" {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if newest != curPath {
			if f != nil {
				f.Close()
			}
			nf, err := os.Open(newest)
			if err != nil {
				sleepCtx(ctx, 2*time.Second)
				continue
			}
			f = nf
			rdr = bufio.NewReader(f)
			curPath = newest
			lastSize = 0
			if !quiet {
				fmt.Println("tailing", filepath.Base(curPath))
			}
		}

		for {
			line, err := rdr.ReadString('\n')
			if line != "" {
				scan(strings.TrimRight(line, "\n"))
			}
			if err == nil {
				continue
			}
			break // EOF or error: wait for more data / reopen below
		}

		if fi, ferr := os.Stat(curPath); ferr == nil {
			if fi.Size() < lastSize { // truncated/rotated in place
				curPath = ""
			} else {
				lastSize = fi.Size()
			}
		} else {
			curPath = ""
		}
		sleepCtx(ctx, 2*time.Second)
	}
}

func runQuotaPoller(ctx context.Context, s *store.Store, eng *rotate.Engine, quiet bool) {
	cfg := s.LoadConfig()
	interval, err := time.ParseDuration(cfg.QuotaPollInterval)
	if err != nil || interval < 1*time.Minute {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 15 * time.Second}
	projID := cloudcode.DefaultProjectID()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		st := s.LoadState()
		active := st.ActiveEmail
		if active == "" {
			continue
		}

		acct, err := s.GetAccount(active)
		if err != nil {
			continue
		}

		// Ensure token is fresh
		now := time.Now()
		exp := acct.File.Token.ExpiryTime()
		if exp.IsZero() || now.Add(60*time.Second).After(exp) {
			rCtx, rCancel := context.WithTimeout(ctx, 20*time.Second)
			rr, rerr := google.Refresh(rCtx, client, acct.File.Token.RefreshToken)
			rCancel()
			if rerr == nil {
				acct.File.Token.AccessToken = rr.AccessToken
				acct.File.Token.Expiry = google.ExpiryFrom(now, rr.ExpiresIn).Format(time.RFC3339)
				if rr.RefreshToken != "" {
					acct.File.Token.RefreshToken = rr.RefreshToken
				}
				_ = s.SaveAccount(acct)
			}
		}

		qCtx, qCancel := context.WithTimeout(ctx, 15*time.Second)
		resp, ferr := cloudcode.FetchAvailableModels(qCtx, client, acct.File.Token.AccessToken, projID)
		qCancel()
		if ferr != nil {
			continue
		}

		groups := cloudcode.GroupModels(resp)
		if len(groups) == 0 {
			continue
		}

		// Update cache
		qc := s.LoadQuotaCache()
		qc.FetchedAt = now.UTC()
		aqc := store.AccountQuotaCache{
			Groups:   map[string]store.QuotaGroupCache{},
			CachedAt: now.UTC(),
		}
		isLow := false
		var lowGroup string
		var lowPct int
		for _, g := range groups {
			aqc.Groups[g.Group] = store.QuotaGroupCache{
				RemainingFraction: g.RemainingFraction,
				ResetTime:         g.ResetTime,
				ModelCount:        g.ModelCount,
			}
			if g.RemainingFraction <= cfg.PreemptThreshold {
				isLow = true
				lowGroup = g.Group
				lowPct = int(g.RemainingFraction * 100)
			}
		}
		qc.Accounts[active] = aqc
		_ = s.SaveQuotaCache(qc)

		// Preemptive rotation if active account quota is critically low
		if isLow {
			if next, err := eng.PickNext(); err == nil && next.Email != active {
				if err := eng.Activate(next); err == nil {
					reason := fmt.Sprintf("preemptive switch: %s group at %d%%", lowGroup, lowPct)
					_ = history.Append(s, history.Event{
						Type:   history.EventRotate,
						Email:  next.Email,
						Reason: reason,
						Detail: fmt.Sprintf("switched from %s", active),
					})
					if !quiet {
						fmt.Printf("[%s] Preemptive rotation: %s (%s is low)\n",
							time.Now().Format("15:04:05"), next.Email, lowGroup)
					}
					if cfg.Notifications {
						notify.Send("agy-rotator", fmt.Sprintf("Preemptive switch to %s (%s at %d%%)", next.Email, lowGroup, lowPct))
					}
				}
			}
		}
	}
}

func newestLog(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type cand struct {
		path string
		mt   time.Time
	}
	var cs []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "cli-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		cs = append(cs, cand{p, fi.ModTime()})
	}
	if len(cs) == 0 {
		return ""
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mt.After(cs[j].mt) })
	return cs[0].path
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
