// Package statusline generates compact, colored status-bar segments for
// embedding in agy's statusLine script or other shell status lines.
package statusline

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorCyan   = "\033[1;36m"
	ColorGreen  = "\033[1;32m"
	ColorYellow = "\033[1;33m"
	ColorRed    = "\033[1;31m"
	ColorMuted  = "\033[0;90m"
)

type StatusPayload struct {
	ActiveEmail  string             `json:"active_email"`
	Username     string             `json:"username"`
	Cooling      bool               `json:"cooling"`
	CoolingFor   string             `json:"cooling_for,omitempty"`
	Failures     int                `json:"failures,omitempty"`
	Groups       map[string]float64 `json:"groups,omitempty"`
	CachedAgeSec int64              `json:"cached_age_sec,omitempty"`
}

// Payload builds a StatusPayload from local store state (zero network calls).
func Payload(s *store.Store) StatusPayload {
	st := s.LoadState()
	email := st.ActiveEmail
	username := email
	if idx := strings.Index(email, "@"); idx > 0 {
		username = email[:idx]
	}

	sp := StatusPayload{
		ActiveEmail: email,
		Username:    username,
	}

	if email != "" {
		as := st.Accounts[email]
		sp.Failures = as.Failures
		if !as.CoolUntil.IsZero() && time.Now().Before(as.CoolUntil) {
			sp.Cooling = true
			sp.CoolingFor = time.Until(as.CoolUntil).Round(time.Second).String()
		}

		qc := s.LoadQuotaCache()
		if aqc, ok := qc.Accounts[email]; ok && len(aqc.Groups) > 0 {
			sp.Groups = map[string]float64{}
			for g, gc := range aqc.Groups {
				sp.Groups[g] = gc.RemainingFraction
			}
			if !qc.FetchedAt.IsZero() {
				sp.CachedAgeSec = int64(time.Since(qc.FetchedAt).Seconds())
			}
		}
	}
	return sp
}

// RenderSegment outputs a compact terminal segment like:
//
//	⬤ alice | C:100% P:73% F:73%
//
// If noColor is true, ANSI escapes are stripped.
func RenderSegment(s *store.Store, noColor bool) string {
	sp := Payload(s)
	if sp.ActiveEmail == "" {
		return ""
	}

	cDot := ColorCyan
	cMuted := ColorMuted
	cReset := ColorReset
	if noColor {
		cDot, cMuted, cReset = "", "", ""
	}

	icon := "⬤"
	if sp.Cooling {
		icon = "❄"
	}

	userPart := fmt.Sprintf("%s%s %s%s", cDot, icon, sp.Username, cReset)
	if sp.Cooling {
		userPart += fmt.Sprintf(" (%s)", sp.CoolingFor)
	}

	if len(sp.Groups) == 0 {
		return userPart
	}

	var groupParts []string
	keys := []struct {
		tag   string
		group string
	}{
		{"C", "claude"},
		{"P", "gemini-pro"},
		{"F", "gemini-flash"},
	}

	for _, k := range keys {
		if frac, ok := sp.Groups[k.group]; ok {
			pct := int(math.Round(frac * 100))
			pctColor := pctColorCode(pct, noColor)
			groupParts = append(groupParts, fmt.Sprintf("%s%s:%s%s%d%%%s", cMuted, k.tag, cReset, pctColor, pct, cReset))
		}
	}

	if len(groupParts) == 0 {
		return userPart
	}

	sep := " "
	return fmt.Sprintf("%s %s%s", userPart, sep, strings.Join(groupParts, " "))
}

func pctColorCode(pct int, noColor bool) string {
	if noColor {
		return ""
	}
	switch {
	case pct >= 50:
		return ColorGreen
	case pct >= 20:
		return ColorYellow
	default:
		return ColorRed
	}
}
