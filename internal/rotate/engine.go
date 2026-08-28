// Package rotate implements the account selection engine: round-robin across
// healthy accounts with per-account cooldowns on failure.
package rotate

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/detect"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

var ErrNoAccounts = errors.New("no accounts in vault; run `agy-rotator add` first")

type Engine struct {
	S *store.Store
}

func New(s *store.Store) *Engine { return &Engine{S: s} }

func (e *Engine) mode() string { return e.S.LoadConfig().Mode }

// PickNext chooses the next account to activate.
// - round-robin: advances the cursor across healthy accounts every call.
// - sticky: keeps the active account unless it is cooling down or revoked.
// - smart: selects the healthy account with highest cached remaining quota.
// Revoked accounts are excluded until fixed; cooling accounts are skipped
// unless all accounts are cooling down (then soonest-expiry wins).
func (e *Engine) PickNext() (*store.Account, error) {
	accounts, err := e.S.ListAccounts()
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAccounts
	}

	st := e.S.LoadState()
	now := time.Now()

	// Exclude revoked/dead accounts
	var eligible []*store.Account
	for _, a := range accounts {
		if !stateFor(st, a.Email).Revoked {
			eligible = append(eligible, a)
		}
	}
	if len(eligible) == 0 {
		return nil, errors.New("all accounts in vault have revoked credentials; run `agy-rotator doctor`")
	}

	idx := map[string]int{}
	for i, a := range eligible {
		idx[a.Email] = i
	}

	order := make([]*store.Account, len(eligible))
	copy(order, eligible)

	switch e.mode() {
	case "smart":
		var healthy []*store.Account
		for _, a := range order {
			if now.After(stateFor(st, a.Email).CoolUntil) {
				healthy = append(healthy, a)
			}
		}
		if len(healthy) > 0 {
			qc := e.S.LoadQuotaCache()
			scoreFor := func(email string) float64 {
				if aqc, ok := qc.Accounts[email]; ok && len(aqc.Groups) > 0 {
					total := 0.0
					for _, g := range aqc.Groups {
						total += g.RemainingFraction
					}
					return total / float64(len(aqc.Groups))
				}
				return 0.5 // neutral baseline if un-queried
			}
			sort.SliceStable(healthy, func(i, j int) bool {
				return scoreFor(healthy[i].Email) > scoreFor(healthy[j].Email)
			})
			return healthy[0], nil
		}

	case "sticky":
		if st.ActiveEmail != "" {
			if _, ok := idx[st.ActiveEmail]; ok && now.After(stateFor(st, st.ActiveEmail).CoolUntil) {
				return accounts[idx[st.ActiveEmail]], nil
			}
		}
	}

	// Round-robin & general healthy scan
	start := ((st.RRIndex % len(order)) + len(order)) % len(order)
	for k := 0; k < len(order); k++ {
		a := order[(start+k)%len(order)]
		if now.After(stateFor(st, a.Email).CoolUntil) {
			e.bumpRR(st, start+k+1, len(order))
			return a, nil
		}
	}

	// Everything cooling: pick earliest-cooling among non-revoked
	sort.SliceStable(order, func(i, j int) bool {
		return stateFor(st, order[i].Email).CoolUntil.Before(stateFor(st, order[j].Email).CoolUntil)
	})
	return order[0], nil
}

func stateFor(st *store.State, email string) store.AccountState {
	return st.Accounts[email]
}

func (e *Engine) bumpRR(st *store.State, next, n int) error {
	st.RRIndex = ((next % n) + n) % n
	return e.S.SaveState(st)
}

// Activate writes acct's tokens as the live credential file and records usage.
func (e *Engine) Activate(acct *store.Account) error {
	tf := acct.File
	if err := e.S.WriteTokenFile(&tf); err != nil {
		return err
	}
	st := e.S.LoadState()
	prev := st.ActiveEmail
	st.ActiveEmail = acct.Email
	as := st.Accounts[acct.Email]
	as.LastUsed = time.Now().UTC()
	as.Failures = 0
	as.CoolUntil = time.Time{}
	st.Accounts[acct.Email] = as
	if prev != acct.Email {
		if ps, ok := st.Accounts[prev]; ok {
			ps.Failures = 0 // fresh start for the rotated-away account once reused later
			st.Accounts[prev] = ps
		}
		_ = history.Append(e.S, history.Event{
			Type:   history.EventRotate,
			Email:  acct.Email,
			Reason: fmt.Sprintf("switched from %s (%s)", orNone(prev), e.mode()),
		})
	} else {
		_ = history.Append(e.S, history.Event{
			Type:   history.EventActivate,
			Email:  acct.Email,
			Reason: "activated",
		})
	}
	return e.S.SaveState(st)
}

// ReportFailure applies the cooldown ladder to an account.
func (e *Engine) ReportFailure(email string, reason detect.Reason) (time.Duration, error) {
	st := e.S.LoadState()
	as := st.Accounts[email]
	as.Failures++
	if reason == detect.AuthDead {
		as.Revoked = true
		st.Accounts[email] = as
		_ = history.Append(e.S, history.Event{
			Type:   history.EventFailure,
			Email:  email,
			Reason: string(reason),
			Detail: "credentials expired/revoked; marked revoked (run agy-rotator doctor)",
		})
		return 24 * time.Hour, e.S.SaveState(st)
	}

	d := detect.BackoffFor(reason, as.Failures-1)
	as.CoolUntil = time.Now().Add(d).UTC()
	st.Accounts[email] = as
	_ = history.Append(e.S, history.Event{
		Type:   history.EventCooldown,
		Email:  email,
		Reason: string(reason),
		Detail: fmt.Sprintf("cooling %s (failure #%d)", d.Truncate(time.Second), as.Failures),
	})
	return d, e.S.SaveState(st)
}

// ReportSuccess clears an account's failure state.
func (e *Engine) ReportSuccess(email string) error {
	st := e.S.LoadState()
	as := st.Accounts[email]
	as.Failures = 0
	as.CoolUntil = time.Time{}
	as.Revoked = false
	st.Accounts[email] = as
	return e.S.SaveState(st)
}

// Status renders a human-readable snapshot.
func (e *Engine) Status() string {
	accounts, err := e.S.ListAccounts()
	if err != nil {
		return fmt.Sprintf("vault error: %v", err)
	}
	st := e.S.LoadState()
	cfg := e.S.LoadConfig()
	var b []byte
	b = append(b, fmt.Sprintf("mode:   %s\n", cfg.Mode)...)
	b = append(b, fmt.Sprintf("active: %s\n", orNone(st.ActiveEmail))...)
	b = append(b, fmt.Sprintf("vault:  %d account(s)\n", len(accounts))...)
	for _, a := range accounts {
		as := stateFor(st, a.Email)
		line := fmt.Sprintf("  %-32s", a.Email)
		switch {
		case a.Email == st.ActiveEmail:
			line += " [active]"
		}
		if as.Revoked {
			line += " [REVOKED - run doctor]"
		} else if !as.CoolUntil.IsZero() && time.Now().Before(as.CoolUntil) {
			line += fmt.Sprintf(" cooling %s (failures=%d)", untilStr(as.CoolUntil), as.Failures)
		} else if as.Failures > 0 {
			line += fmt.Sprintf(" failures=%d (cleared next success)", as.Failures)
		}
		if a.Label != "" {
			line += "  # " + a.Label
		}
		b = append(b, line+"\n"...)
	}
	return string(b)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func untilStr(t time.Time) string {
	d := time.Until(t).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Truncate(time.Second).String()
}
