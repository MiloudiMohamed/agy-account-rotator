// Command agy-rotator rotates multiple Google accounts for the Antigravity
// CLI (agy) so you never repeat browser logins when one account's quota runs
// out. See README.md.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/agyplugin"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/cloudcode"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/completions"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/google"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/proxy"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/rotate"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/stats"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/statusline"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/vaultcrypto"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/watch"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/wizard"
)

const version = "0.1.0"

const shimTemplate = `#!/bin/sh
# Installed by agy-rotator. Rotates to the next vaulted account, then launches
# the real agy with in-flight proxy support.
if command -v agy-rotator >/dev/null 2>&1; then
    agy-rotator prelaunch >/dev/null 2>&1 || true
fi
CA_PEM="${XDG_DATA_HOME:-$HOME/.local/share}/agy-rotator/ca.pem"
if [ -f "$CA_PEM" ] && [ "${AGY_ROTATOR_NO_PROXY:-0}" != "1" ] && [ "${AGY_ROTATOR_DISABLE:-0}" != "1" ]; then
    export HTTP_PROXY="http://127.0.0.1:8999"
    export HTTPS_PROXY="http://127.0.0.1:8999"
    export ALL_PROXY="http://127.0.0.1:8999"
    export SSL_CERT_FILE="$CA_PEM"
    export NODE_EXTRA_CA_CERTS="$CA_PEM"
    export GRPC_DEFAULT_SSL_ROOTS_FILE_PATH="$CA_PEM"
fi
exec %q "$@"
`

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "add":
		err = cmdAdd(args)
	case "list", "ls":
		err = cmdList()
	case "status":
		err = withEngine(func(s *store.Store, e *rotate.Engine) error {
			fmt.Print(e.Status())
			return nil
		})
	case "rotate":
		err = withEngine(func(s *store.Store, e *rotate.Engine) error {
			a, err := e.PickNext()
			if err != nil {
				return err
			}
			if err := e.Activate(a); err != nil {
				return err
			}
			fmt.Println("activated:", a.Email)
			return nil
		})
	case "use":
		err = cmdUse(args)
	case "remove", "rm":
		err = cmdRemove(args)
	case "doctor":
		err = cmdDoctor(args)
	case "quota":
		err = cmdQuota(args)
	case "stats":
		err = cmdStats(args)
	case "export":
		err = cmdExport(args)
	case "import":
		err = cmdImport(args)
	case "history", "log":
		err = cmdHistory(args)
	case "why":
		err = cmdWhy(args)
	case "statusline":
		err = cmdStatusline(args)
	case "config":
		err = cmdConfig(args)
	case "proxy":
		err = cmdProxy(args)
	case "prelaunch":
		err = cmdPrelaunch()
	case "watch":
		err = cmdWatch(args)
	case "set-mode":
		err = cmdSetMode(args)
	case "plugin":
		err = cmdPlugin(args)
	case "shim":
		err = cmdShim(args)
	case "completions":
		err = cmdCompletions(args)
	case "version", "-v", "--version":
		fmt.Println("agy-rotator", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`agy-rotator — multi-account rotation for Antigravity CLI (agy)

Usage:
  agy-rotator add [-label L]        Capture account(s): drives agy login per Google account
  agy-rotator list                  List vaulted accounts
  agy-rotator status                Show active account + cooldowns
  agy-rotator quota [-email E]      Preview remaining quota per model for all (or one) account
  agy-rotator stats                 Show local CLI conversation counts, steps & usage metrics
  agy-rotator why                   Explain why current account is active & selection state
  agy-rotator history [-n N]        Audit log of rotations, cooldowns, and failures
  agy-rotator statusline [--no-color]
                                    Emit compact status-bar segment (reads local cache, 0ms)
  agy-rotator proxy [start|stop|status|daemon|cert]
                                    Manage in-flight transparent request proxy (auto-starts on agy launch)
  agy-rotator rotate                Switch live credentials to the next account now
  agy-rotator use -email E          Activate a specific account
  agy-rotator remove -email E       Forget an account
  agy-rotator doctor [-email E] [-fix]
                                    Re-validate stored tokens (with -fix to prune revoked accounts)
  agy-rotator export [-out file]    Export passphrase-encrypted vault envelope
  agy-rotator import [-replace] [f] Import accounts from encrypted vault envelope
  agy-rotator config [get|set]      View or update configuration settings
  agy-rotator watch [install-service|uninstall-service|status-service]
                                    Auto-cooldown + proactive quota rotation (or background service)
  agy-rotator set-mode <m>          round-robin (default) | sticky | smart
  agy-rotator plugin install|uninstall|status
                                    Optional: teach the agy agent about this tool
  agy-rotator completions [zsh|bash|install]
                                    Output or install shell tab-completions
  agy-rotator prelaunch             Internal: rotate & ensure proxy before launching agy
  agy-rotator shim install|uninstall|print [--write-rc]
                                    Manage PATH shim so every ` + "`agy`" + ` launch rotates

Env:
  AGY_BIN=/path/to/agy              Override real binary path
  AGY_ROTATOR_DISABLE=1             Make the shim a pass-through
  AGY_ROTATOR_NO_PROXY=1            Disable transparent proxy interception
`)
}

func withEngine(fn func(*store.Store, *rotate.Engine) error) error {
	s, err := store.Open()
	if err != nil {
		return err
	}
	return fn(s, rotate.New(s))
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	label := fs.String("label", "", "friendly label stored with the account")
	fs.Parse(args)
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		return wizard.Add(s, *label, e)
	})
}

func cmdList() error {
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		accounts, err := s.ListAccounts()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Println("vault is empty — run: agy-rotator add")
			return nil
		}
		st := s.LoadState()
		for i, a := range accounts {
			mark := " "
			if a.Email == st.ActiveEmail {
				mark = "*"
			}
			suffix := ""
			if a.Label != "" {
				suffix = "  # " + a.Label
			}
			fmt.Printf("%d%s %s%s\n", i+1, mark, a.Email, suffix)
		}
		return nil
	})
}

func cmdUse(args []string) error {
	fs := flag.NewFlagSet("use", flag.ExitOnError)
	email := fs.String("email", "", "account email to activate")
	fs.Parse(args)
	if *email == "" {
		return fmt.Errorf("-email is required")
	}
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		a, err := s.GetAccount(*email)
		if err != nil {
			return fmt.Errorf("no such account in vault: %s", *email)
		}
		if err := e.Activate(a); err != nil {
			return err
		}
		fmt.Println("activated:", a.Email)
		return nil
	})
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	email := fs.String("email", "", "account email to forget")
	fs.Parse(args)
	if *email == "" {
		return fmt.Errorf("-email is required")
	}
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		if err := s.RemoveAccount(*email); err != nil {
			return err
		}
		_ = history.Append(s, history.Event{
			Type:  history.EventRemove,
			Email: *email,
		})
		st := s.LoadState()
		if st.ActiveEmail == *email {
			st.ActiveEmail = ""
			if err := s.SaveState(st); err != nil {
				return err
			}
		}
		delete(st.Accounts, *email)
		return s.SaveState(st)
	})
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	email := fs.String("email", "", "check only this account")
	fix := fs.Bool("fix", false, "automatically prune accounts with permanently revoked credentials")
	fs.Parse(args)
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		client := &http.Client{Timeout: 20 * time.Second}
		accounts, err := s.ListAccounts()
		if err != nil {
			return err
		}
		st := s.LoadState()
		var failures int
		for _, a := range accounts {
			if *email != "" && a.Email != *email {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			rr, err := google.Refresh(ctx, client, a.File.Token.RefreshToken)
			cancel()
			if err != nil {
				failures++
				if *fix && (strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "revoked") || strings.Contains(err.Error(), "expired")) {
					_ = s.RemoveAccount(a.Email)
					delete(st.Accounts, a.Email)
					if st.ActiveEmail == a.Email {
						st.ActiveEmail = ""
					}
					_ = s.SaveState(st)
					_ = history.Append(s, history.Event{
						Type:   history.EventRemove,
						Email:  a.Email,
						Reason: "pruned by doctor --fix",
						Detail: err.Error(),
					})
					fmt.Printf("PRUNED %-32s (%v)\n", a.Email, err)
				} else {
					fmt.Printf("FAIL   %-32s %v\n", a.Email, err)
				}
				continue
			}
			oldRefresh := a.File.Token.RefreshToken
			wasActive := a.Email == st.ActiveEmail
			a.File.Token.AccessToken = rr.AccessToken
			a.File.Token.Expiry = google.ExpiryFrom(time.Now(), rr.ExpiresIn).Format(time.RFC3339)
			if rr.RefreshToken != "" && rr.RefreshToken != oldRefresh {
				a.File.Token.RefreshToken = rr.RefreshToken // Google rotated it
				fmt.Printf("OK     %-32s (refresh token rotated; updated)\n", a.Email)
			} else {
				fmt.Printf("OK     %-32s\n", a.Email)
			}
			if err := s.SaveAccount(a); err != nil {
				return err
			}
			_ = e.ReportSuccess(a.Email)
			_ = history.Append(s, history.Event{
				Type:   history.EventRevalidate,
				Email:  a.Email,
				Reason: "token refreshed and valid",
			})
			// refresh the live file too, but only if we won't clobber newer state
			if wasActive {
				if live, lerr := s.ReadTokenFile(); lerr != nil || live.Token.RefreshToken == oldRefresh {
					tf := a.File
					if lerr == nil && tf.AuthMethod == "" {
						tf.AuthMethod = live.AuthMethod
					}
					_ = s.WriteTokenFile(&tf)
				}
			}
		}
		if failures > 0 {
			return fmt.Errorf("%d account(s) failed revalidation", failures)
		}
		return nil
	})
}

func cmdQuota(args []string) error {
	fs := flag.NewFlagSet("quota", flag.ExitOnError)
	email := fs.String("email", "", "check quota for a specific email (default: all accounts)")
	asJSON := fs.Bool("json", false, "output raw JSON")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		accounts, err := s.ListAccounts()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return fmt.Errorf("vault is empty — run: agy-rotator add")
		}

		st := s.LoadState()
		projID := cloudcode.DefaultProjectID()
		client := &http.Client{Timeout: 15 * time.Second}

		var results []cloudcode.AccountQuota
		now := time.Now()

		for _, a := range accounts {
			if *email != "" && a.Email != *email {
				continue
			}
			aq := cloudcode.AccountQuota{
				Email:    a.Email,
				Active:   a.Email == st.ActiveEmail,
				Failures: st.Accounts[a.Email].Failures,
			}
			if cu := st.Accounts[a.Email].CoolUntil; !cu.IsZero() && now.Before(cu) {
				aq.Cooling = time.Until(cu).Round(time.Second).String()
			}

			// Ensure token is fresh: if expired or expiring within 60s, refresh
			exp := a.File.Token.ExpiryTime()
			if exp.IsZero() || now.Add(60*time.Second).After(exp) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				rr, rerr := google.Refresh(ctx, client, a.File.Token.RefreshToken)
				cancel()
				if rerr == nil {
					a.File.Token.AccessToken = rr.AccessToken
					a.File.Token.Expiry = google.ExpiryFrom(now, rr.ExpiresIn).Format(time.RFC3339)
					if rr.RefreshToken != "" {
						a.File.Token.RefreshToken = rr.RefreshToken
					}
					_ = s.SaveAccount(a)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			resp, ferr := cloudcode.FetchAvailableModels(ctx, client, a.File.Token.AccessToken, projID)
			cancel()

			// If 401, retry once after fresh refresh
			if ferr != nil && (strings.Contains(ferr.Error(), "401") || strings.Contains(ferr.Error(), "UNAUTHENTICATED")) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				rr, rerr := google.Refresh(ctx, client, a.File.Token.RefreshToken)
				cancel()
				if rerr == nil {
					a.File.Token.AccessToken = rr.AccessToken
					a.File.Token.Expiry = google.ExpiryFrom(now, rr.ExpiresIn).Format(time.RFC3339)
					if rr.RefreshToken != "" {
						a.File.Token.RefreshToken = rr.RefreshToken
					}
					_ = s.SaveAccount(a)

					ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
					resp, ferr = cloudcode.FetchAvailableModels(ctx2, client, a.File.Token.AccessToken, projID)
					cancel2()
				}
			}

			if ferr != nil {
				aq.Error = ferr.Error()
			} else {
				aq.Groups = cloudcode.GroupModels(resp)
			}
			results = append(results, aq)
		}

		if len(results) == 0 && *email != "" {
			return fmt.Errorf("no account %q in vault", *email)
		}

		// Save results to persistent quota cache for statusline/why/smart mode
		qc := s.LoadQuotaCache()
		qc.FetchedAt = now.UTC()
		for _, res := range results {
			aqc := store.AccountQuotaCache{
				Groups:   map[string]store.QuotaGroupCache{},
				Error:    res.Error,
				CachedAt: now.UTC(),
			}
			for _, g := range res.Groups {
				aqc.Groups[g.Group] = store.QuotaGroupCache{
					RemainingFraction: g.RemainingFraction,
					ResetTime:         g.ResetTime,
					ModelCount:        g.ModelCount,
				}
			}
			qc.Accounts[res.Email] = aqc
		}
		_ = s.SaveQuotaCache(qc)

		if *asJSON {
			b, _ := json.MarshalIndent(results, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		for i, res := range results {
			if i > 0 {
				fmt.Println()
			}
			header := res.Email
			if res.Active {
				header += " [active]"
			}
			if res.Cooling != "" {
				header += fmt.Sprintf(" (cooling %s, failures=%d)", res.Cooling, res.Failures)
			}
			fmt.Println(header)

			if res.Error != "" {
				fmt.Printf("  ✗ %s\n", res.Error)
				continue
			}
			if len(res.Groups) == 0 {
				fmt.Println("  (no quota buckets reported)")
				continue
			}
			for _, g := range res.Groups {
				pct := int(math.Round(g.RemainingFraction * 100))
				bar := cloudcode.ProgressBar(g.RemainingFraction)
				resetInfo := ""
				if g.ResetTime != "" {
					if rStr := cloudcode.FormatReset(g.ResetTime, now); rStr != "" {
						resetInfo = "  " + rStr
					}
				}
				fmt.Printf("  %-14s %s %3d%%%s\n", g.Group, bar, pct, resetInfo)
			}
		}
		return nil
	})
}

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	limit := fs.Int("n", 20, "max number of events to show")
	email := fs.String("email", "", "filter events by account email")
	asJSON := fs.Bool("json", false, "output raw JSON")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		events, err := history.Read(s, *limit, *email)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			fmt.Println("no history logged yet")
			return nil
		}
		if *asJSON {
			b, _ := json.MarshalIndent(events, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		for _, ev := range events {
			fmt.Println(history.Format(ev))
		}
		return nil
	})
}

type WhyReport struct {
	ActiveEmail string         `json:"active_email"`
	Mode        string         `json:"mode"`
	RRIndex     int            `json:"rr_index"`
	Accounts    []WhyAccount   `json:"accounts"`
	LastEvent   *history.Event `json:"last_event,omitempty"`
	QuotaCache  *WhyQuotaCache `json:"quota_cache,omitempty"`
}

type WhyAccount struct {
	Email    string `json:"email"`
	Active   bool   `json:"active"`
	Status   string `json:"status"`
	Failures int    `json:"failures,omitempty"`
	Cooling  string `json:"cooling,omitempty"`
	LastUsed string `json:"last_used,omitempty"`
}

type WhyQuotaCache struct {
	FetchedAt string `json:"fetched_at"`
	Age       string `json:"age"`
}

func cmdWhy(args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		accounts, err := s.ListAccounts()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return fmt.Errorf("vault is empty — run: agy-rotator add")
		}

		st := s.LoadState()
		cfg := s.LoadConfig()
		now := time.Now()

		report := WhyReport{
			ActiveEmail: st.ActiveEmail,
			Mode:        cfg.Mode,
			RRIndex:     st.RRIndex,
		}

		for _, a := range accounts {
			as := st.Accounts[a.Email]
			wa := WhyAccount{
				Email:    a.Email,
				Active:   a.Email == st.ActiveEmail,
				Failures: as.Failures,
			}
			if !as.LastUsed.IsZero() {
				wa.LastUsed = as.LastUsed.Local().Format("2006-01-02 15:04:05")
			}
			if !as.CoolUntil.IsZero() && now.Before(as.CoolUntil) {
				wa.Status = "cooling"
				wa.Cooling = time.Until(as.CoolUntil).Round(time.Second).String()
			} else if as.Failures > 0 {
				wa.Status = fmt.Sprintf("failures=%d", as.Failures)
			} else {
				wa.Status = "healthy"
			}
			report.Accounts = append(report.Accounts, wa)
		}

		lastEv, _ := history.LastEvent(s, "")
		report.LastEvent = lastEv

		qc := s.LoadQuotaCache()
		if !qc.FetchedAt.IsZero() {
			report.QuotaCache = &WhyQuotaCache{
				FetchedAt: qc.FetchedAt.Local().Format("2006-01-02 15:04:05"),
				Age:       time.Since(qc.FetchedAt).Round(time.Second).String() + " ago",
			}
		}

		if *asJSON {
			b, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		fmt.Printf("active:   %s\n", orNone(st.ActiveEmail))
		fmt.Printf("mode:     %s (slot %d/%d)\n", cfg.Mode, st.RRIndex+1, len(accounts))
		fmt.Println("\naccounts:")
		for _, wa := range report.Accounts {
			mark := " "
			if wa.Active {
				mark = "*"
			}
			detail := wa.Status
			if wa.Cooling != "" {
				detail = fmt.Sprintf("cooling %s (failures=%d)", wa.Cooling, wa.Failures)
			}
			if wa.LastUsed != "" {
				detail += fmt.Sprintf(" — last used %s", wa.LastUsed)
			}
			fmt.Printf("  %s %-32s  %s\n", mark, wa.Email, detail)
		}

		if report.LastEvent != nil {
			fmt.Println("\nlast event:")
			fmt.Printf("  %s\n", history.Format(*report.LastEvent))
		}

		if report.QuotaCache != nil {
			fmt.Println("\nquota cache:")
			fmt.Printf("  updated %s (run 'agy-rotator quota' to refresh)\n", report.QuotaCache.Age)
		} else {
			fmt.Println("\nquota cache:")
			fmt.Println("  none cached (run 'agy-rotator quota' to fetch)")
		}

		return nil
	})
}

func cmdStatusline(args []string) error {
	fs := flag.NewFlagSet("statusline", flag.ExitOnError)
	noColor := fs.Bool("no-color", false, "disable ANSI color escapes")
	asJSON := fs.Bool("json", false, "output raw JSON payload")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		if *asJSON {
			payload := statusline.Payload(s)
			b, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		seg := statusline.RenderSegment(s, *noColor)
		if seg != "" {
			fmt.Print(seg)
		}
		return nil
	})
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		rep, err := stats.Collect(s)
		if err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		fmt.Printf("Conversations:    %d", rep.TotalConversations)
		if rep.TotalConversations > 0 {
			fmt.Printf(" (%d active today, %d this week)", rep.ActiveToday, rep.ActiveThisWeek)
		}
		fmt.Println()
		if rep.TotalSteps > 0 {
			fmt.Printf("Total Steps:      %d\n", rep.TotalSteps)
		}
		fmt.Printf("Storage Size:     %s\n", stats.FormatStorage(rep.TotalStorageBytes))
		if !rep.FirstActive.IsZero() && !rep.LastActive.IsZero() {
			fmt.Printf("Activity Span:    %s to %s\n",
				rep.FirstActive.Local().Format("2006-01-02"),
				rep.LastActive.Local().Format("2006-01-02"))
		}
		fmt.Printf("Rotations Logged: %d (%d cooldowns triggered)\n", rep.TotalRotations, rep.TotalCooldowns)

		if len(rep.AccountSummaries) > 0 {
			fmt.Println("\nVault Accounts:")
			for _, as := range rep.AccountSummaries {
				mark := " "
				if as.Active {
					mark = "*"
				}
				detail := as.Status
				if as.Switches > 0 {
					detail += fmt.Sprintf(" (%d switches)", as.Switches)
				}
				if !as.LastUsed.IsZero() {
					detail += " — last used " + as.LastUsed.Local().Format("2006-01-02 15:04")
				}
				fmt.Printf("  %s %-32s %s\n", mark, as.Email, detail)
			}
		}
		return nil
	})
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	passphrase := fs.String("passphrase", "", "passphrase to encrypt export envelope")
	outFile := fs.String("out", "", "output file path (default: stdout)")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		accounts, err := s.ListAccounts()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return fmt.Errorf("vault is empty — run: agy-rotator add")
		}

		pwd := *passphrase
		if pwd == "" {
			fmt.Print("Enter passphrase to encrypt vault: ")
			r := bufio.NewReader(os.Stdin)
			p, _ := r.ReadString('\n')
			pwd = strings.TrimSpace(p)
			if pwd == "" {
				return fmt.Errorf("passphrase cannot be empty")
			}
		}

		payload := &vaultcrypto.ExportPayload{
			ExportedAt: time.Now().UTC(),
			Version:    version,
			Accounts:   accounts,
			Config:     s.LoadConfig(),
		}

		encrypted, err := vaultcrypto.EncryptVault(payload, pwd)
		if err != nil {
			return fmt.Errorf("encrypting vault: %w", err)
		}

		if *outFile != "" {
			if err := os.WriteFile(*outFile, encrypted, 0o600); err != nil {
				return err
			}
			fmt.Printf("✓ exported %d account(s) encrypted to %s\n", len(accounts), *outFile)
			return nil
		}

		fmt.Println(string(encrypted))
		return nil
	})
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	passphrase := fs.String("passphrase", "", "passphrase to decrypt import envelope")
	replace := fs.Bool("replace", false, "replace all existing accounts instead of merging")
	fs.Parse(args)

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		var input []byte
		var err error

		if fs.NArg() > 0 {
			input, err = os.ReadFile(fs.Arg(0))
			if err != nil {
				return fmt.Errorf("reading import file: %w", err)
			}
		} else {
			// Read from stdin
			input, err = io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
		}

		if len(input) == 0 {
			return fmt.Errorf("no input provided (specify file or pipe envelope into stdin)")
		}

		pwd := *passphrase
		if pwd == "" {
			fmt.Print("Enter passphrase to decrypt vault: ")
			r := bufio.NewReader(os.Stdin)
			p, _ := r.ReadString('\n')
			pwd = strings.TrimSpace(p)
			if pwd == "" {
				return fmt.Errorf("passphrase cannot be empty")
			}
		}

		payload, err := vaultcrypto.DecryptVault(input, pwd)
		if err != nil {
			return fmt.Errorf("decrypting vault: %w", err)
		}

		if *replace {
			existing, _ := s.ListAccounts()
			for _, a := range existing {
				_ = s.RemoveAccount(a.Email)
			}
		}

		importedCount := 0
		for _, a := range payload.Accounts {
			if err := s.SaveAccount(a); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not save %s: %v\n", a.Email, err)
				continue
			}
			importedCount++
			_ = history.Append(s, history.Event{
				Type:   history.EventAdd,
				Email:  a.Email,
				Detail: "imported from backup",
			})
		}

		// If no active account is currently set, activate the first imported one
		st := s.LoadState()
		if st.ActiveEmail == "" && len(payload.Accounts) > 0 {
			_ = e.Activate(payload.Accounts[0])
		}

		fmt.Printf("✓ successfully imported %d account(s) into vault\n", importedCount)
		return nil
	})
}

func cmdConfig(args []string) error {
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		cfg := s.LoadConfig()

		if len(args) == 0 || args[0] == "list" {
			b, _ := json.MarshalIndent(cfg, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		switch args[0] {
		case "get":
			if len(args) < 2 {
				return fmt.Errorf("usage: agy-rotator config get <key>")
			}
			switch args[1] {
			case "mode":
				fmt.Println(cfg.Mode)
			case "preempt_threshold":
				fmt.Printf("%.2f\n", cfg.PreemptThreshold)
			case "quota_poll_interval":
				fmt.Println(cfg.QuotaPollInterval)
			case "notifications":
				fmt.Println(cfg.Notifications)
			case "proxy_enabled":
				fmt.Println(cfg.ProxyEnabled)
			case "proxy_port":
				fmt.Println(cfg.ProxyPort)
			case "proxy_idle_timeout":
				fmt.Println(cfg.ProxyIdleTimeout)
			default:
				return fmt.Errorf("unknown config key %q", args[1])
			}
			return nil

		case "set":
			if len(args) < 3 {
				return fmt.Errorf("usage: agy-rotator config set <key> <value>")
			}
			key, val := args[1], args[2]
			switch key {
			case "mode":
				if val != "round-robin" && val != "sticky" && val != "smart" {
					return fmt.Errorf("invalid mode %q (options: round-robin, sticky, smart)", val)
				}
				cfg.Mode = val
			case "preempt_threshold":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f <= 0 || f >= 1 {
					return fmt.Errorf("invalid threshold %q (must be a float between 0.01 and 0.99)", val)
				}
				cfg.PreemptThreshold = f
			case "quota_poll_interval":
				if _, err := time.ParseDuration(val); err != nil {
					return fmt.Errorf("invalid duration %q (e.g. 5m, 10m)", val)
				}
				cfg.QuotaPollInterval = val
			case "notifications":
				b, err := strconv.ParseBool(val)
				if err != nil {
					return fmt.Errorf("invalid boolean %q (true/false)", val)
				}
				cfg.Notifications = b
			case "proxy_enabled":
				b, err := strconv.ParseBool(val)
				if err != nil {
					return fmt.Errorf("invalid boolean %q (true/false)", val)
				}
				cfg.ProxyEnabled = b
			case "proxy_port":
				p, err := strconv.Atoi(val)
				if err != nil || p <= 0 || p > 65535 {
					return fmt.Errorf("invalid port %q (1-65535)", val)
				}
				cfg.ProxyPort = p
			case "proxy_idle_timeout":
				if _, err := time.ParseDuration(val); err != nil {
					return fmt.Errorf("invalid duration %q (e.g. 30m, 1h)", val)
				}
				cfg.ProxyIdleTimeout = val
			default:
				return fmt.Errorf("unknown config key %q", key)
			}
			if err := s.SaveConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("✓ %s set to %v\n", key, val)
			return nil

		default:
			return fmt.Errorf("unknown config subcommand %q (use get or set)", args[0])
		}
	})
}

func cmdProxy(args []string) error {
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}

	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		cfg := s.LoadConfig()
		port := cfg.ProxyPort
		if port <= 0 {
			port = 8999
		}

		switch action {
		case "daemon":
			cm, err := proxy.LoadOrCreateCA(s)
			if err != nil {
				return err
			}
			idleTimeout, err := time.ParseDuration(cfg.ProxyIdleTimeout)
			if err != nil || idleTimeout <= 0 {
				idleTimeout = 60 * time.Minute
			}
			srv := proxy.NewServer(s, e, cm, port, idleTimeout)
			return srv.Start()

		case "start":
			if proxy.IsRunning(port) {
				fmt.Printf("proxy is already running on 127.0.0.1:%d\n", port)
				return nil
			}
			if err := proxy.EnsureRunning(s); err != nil {
				return err
			}
			fmt.Printf("✓ started background proxy on 127.0.0.1:%d\n", port)
			return nil

		case "stop":
			if !proxy.IsRunning(port) {
				fmt.Println("proxy is not running")
				return nil
			}
			if err := proxy.Stop(s); err != nil {
				return err
			}
			fmt.Println("✓ stopped proxy")
			return nil

		case "status":
			if !proxy.IsRunning(port) {
				fmt.Printf("proxy: not running (configured on 127.0.0.1:%d, auto-starts on agy launch)\n", port)
				return nil
			}
			hr, err := proxy.FetchHealth(port)
			if err != nil {
				fmt.Printf("proxy: running on 127.0.0.1:%d (failed to query details: %v)\n", port, err)
				return nil
			}
			fmt.Printf("proxy:       running on 127.0.0.1:%d (PID %d)\n", hr.Port, hr.PID)
			fmt.Printf("uptime:      %s\n", hr.Uptime)
			fmt.Printf("active:      %s (%s mode)\n", orNone(hr.ActiveEmail), hr.Mode)
			fmt.Printf("requests:    %d\n", hr.RequestCount)
			fmt.Printf("ca cert:     %s\n", s.CAPath())
			return nil

		case "cert":
			cm, err := proxy.LoadOrCreateCA(s)
			if err != nil {
				return err
			}
			_ = cm
			fmt.Printf("CA certificate: %s\n", s.CAPath())
			fmt.Printf("CA private key: %s\n", s.CAKeyPath())
			return nil

		default:
			return fmt.Errorf("unknown proxy action %q (use start, daemon, stop, status, or cert)", action)
		}
	})
}

func cmdPrelaunch() error {
	if os.Getenv("AGY_ROTATOR_DISABLE") == "1" {
		return nil
	}
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		a, err := e.PickNext()
		if err == nil {
			_ = e.Activate(a)
		}
		if os.Getenv("AGY_ROTATOR_NO_PROXY") != "1" {
			_ = proxy.EnsureRunning(s)
		}
		return nil
	})
}

func cmdWatch(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install-service":
			return installSystemdService()
		case "uninstall-service":
			return uninstallSystemdService()
		case "status-service":
			return statusSystemdService()
		}
	}

	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "only report failures, do not auto-switch")
	fs.Parse(args)
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		fmt.Println("watching agy logs & polling quota — ctrl-c to stop")
		return watch.Run(context.Background(), s, e, *quiet)
	})
}

func installSystemdService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binPath := filepath.Join(home, ".local", "bin", "agy-rotator")
	if _, err := os.Stat(binPath); err != nil {
		if lp, lerr := exec.LookPath("agy-rotator"); lerr == nil {
			binPath = lp
		}
	}

	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return err
	}

	serviceFile := filepath.Join(serviceDir, "agy-rotator-watch.service")
	unitContent := fmt.Sprintf(`[Unit]
Description=Antigravity CLI Account Rotator Watcher
After=network.target

[Service]
Type=simple
ExecStart=%s watch
Restart=always
RestartSec=5s
Environment=PATH=%s/bin:/usr/local/bin:/usr/bin:/bin:%s/.local/bin

[Install]
WantedBy=default.target
`, binPath, home, home)

	if err := os.WriteFile(serviceFile, []byte(unitContent), 0o644); err != nil {
		return err
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	cmd := exec.Command("systemctl", "--user", "enable", "--now", "agy-rotator-watch.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("enabling service: %s (%w)", string(out), err)
	}

	fmt.Println("✓ installed and started systemd user service: agy-rotator-watch.service")
	fmt.Println("  (logs: journalctl --user -u agy-rotator-watch -f)")
	return nil
}

func uninstallSystemdService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	serviceFile := filepath.Join(home, ".config", "systemd", "user", "agy-rotator-watch.service")

	_ = exec.Command("systemctl", "--user", "disable", "--now", "agy-rotator-watch.service").Run()
	_ = os.Remove(serviceFile)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()

	fmt.Println("✓ disabled and removed agy-rotator-watch.service")
	return nil
}

func statusSystemdService() error {
	cmd := exec.Command("systemctl", "--user", "status", "agy-rotator-watch.service")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func cmdSetMode(args []string) error {
	if len(args) != 1 || (args[0] != "round-robin" && args[0] != "sticky" && args[0] != "smart") {
		return fmt.Errorf("usage: agy-rotator set-mode round-robin|sticky|smart")
	}
	return withEngine(func(s *store.Store, e *rotate.Engine) error {
		c := s.LoadConfig()
		c.Mode = args[0]
		if err := s.SaveConfig(c); err != nil {
			return err
		}
		fmt.Println("mode set to", c.Mode)
		return nil
	})
}

func cmdPlugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agy-rotator plugin install|uninstall|status")
	}
	switch args[0] {
	case "install":
		n, err := agyplugin.Install()
		if err != nil {
			return err
		}
		fmt.Printf("✓ installed %d file(s) to %s\n", n, agyplugin.Dest())
		fmt.Println("  (restart any running 'agy' sessions to pick up the skill)")
	case "uninstall":
		if err := agyplugin.Uninstall(); err != nil {
			return err
		}
		fmt.Println("removed", agyplugin.Dest())
	case "status":
		fmt.Println(agyplugin.String())
	default:
		return fmt.Errorf("unknown plugin action %q", args[0])
	}
	return nil
}

func cmdCompletions(args []string) error {
	target := "zsh"
	if len(args) > 0 {
		target = args[0]
	}
	switch target {
	case "install":
		p, err := completions.Install("")
		if err != nil {
			return err
		}
		fmt.Println("✓ installed completion script to", p)
		fmt.Println("  (restart your shell or run 'autoload -U compinit && compinit')")
		return nil
	case "zsh":
		fmt.Print(completions.ZshScript)
		return nil
	case "bash":
		fmt.Print(completions.BashScript)
		return nil
	default:
		return fmt.Errorf("unknown completion target %q (use zsh, bash, or install)", target)
	}
}

func cmdShim(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agy-rotator shim install|uninstall|print [--write-rc]")
	}
	action := args[0]
	writeRC := false
	for _, a := range args[1:] {
		if a == "--write-rc" {
			writeRC = true
		}
	}

	home, _ := os.UserHomeDir()
	binDir := filepath.Join(home, ".agy-rotator", "bin")
	shimPath := filepath.Join(binDir, "agy")

	switch action {
	case "print":
		fmt.Printf("shim path : %s\nreal binary: %s\nPATH line :\n  export PATH=%q:$PATH\n",
			shimPath, store.AgyBinPath(), binDir)
		return nil
	case "uninstall":
		if err := os.Remove(shimPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("removed", shimPath, "(remove the PATH line from your shell rc if added)")
		return nil
	case "install":
		real := store.AgyBinPath()
		if abs, err := filepath.Abs(real); err == nil {
			real = abs
		}
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return err
		}
		script := fmt.Sprintf(shimTemplate, real)
		if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
			return err
		}
		line := fmt.Sprintf(`export PATH="%s:$PATH" # added by agy-rotator`, binDir)
		fmt.Println("installed", shimPath, "->", real)
		if writeRC {
			rc := rcPath()
			data, _ := os.ReadFile(rc)
			if !strings.Contains(string(data), "# added by agy-rotator") {
				f, err := os.OpenFile(rc, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				fmt.Fprintf(f, "\n%s\n", line)
				f.Close()
				fmt.Println("appended PATH export to", rc)
			} else {
				fmt.Println("PATH export already present in", rc)
			}
		} else {
			fmt.Printf("Add this to your shell rc to activate (or rerun with --write-rc):\n  %s\n", line)
		}
		return nil
	default:
		return fmt.Errorf("unknown shim action %q", action)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func rcPath() string {
	shell := os.Getenv("SHELL")
	home, _ := os.UserHomeDir()
	name := ".bashrc"
	if strings.Contains(shell, "zsh") {
		name = ".zshrc"
	}
	return filepath.Join(home, name)
}
