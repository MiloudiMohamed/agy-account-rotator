// Package store manages the account vault and rotation state on disk.
//
// Layout:
//
//	$XDG_DATA_HOME/agy-rotator/            (default ~/.local/share/agy-rotator)
//	  config.json                          mode + knobs
//	  state.json                           active account, rr index, per-account cooldowns
//	  accounts/<slug>.json                 one file per Google account
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TokenFile mirrors agy's ~/.gemini/antigravity-cli/antigravity-oauth-token exactly.
type TokenFile struct {
	Token      Token  `json:"token"`
	AuthMethod string `json:"auth_method"`
}

type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry"` // RFC3339
}

func (t Token) ExpiryTime() time.Time {
	if e, err := time.Parse(time.RFC3339, t.Expiry); err == nil {
		return e
	}
	return time.Time{}
}

// Account is one vaulted Google account.
type Account struct {
	Email   string    `json:"email"`
	Label   string    `json:"label,omitempty"`
	AddedAt time.Time `json:"added_at"`
	File    TokenFile `json:"token_file"`
}

// AccountState tracks health per account.
type AccountState struct {
	Failures  int       `json:"failures"`
	CoolUntil time.Time `json:"cool_until,omitempty"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Revoked   bool      `json:"revoked,omitempty"`
}

// State is the mutable rotation state.
type State struct {
	ActiveEmail string                  `json:"active_email"`
	RRIndex     int                     `json:"rr_index"`
	Accounts    map[string]AccountState `json:"accounts"`
}

// Config holds user-tunable knobs.
type Config struct {
	Mode              string  `json:"mode"`                // "round-robin" | "sticky" | "smart"
	PreemptThreshold  float64 `json:"preempt_threshold"`   // e.g. 0.10 (10%)
	QuotaPollInterval string  `json:"quota_poll_interval"` // e.g. "5m"
	Notifications     bool    `json:"notifications"`       // enable desktop notifications
	ProxyEnabled      bool    `json:"proxy_enabled"`       // transparent in-flight request proxy
	ProxyPort         int     `json:"proxy_port"`          // local proxy port (default 8999)
	ProxyIdleTimeout  string  `json:"proxy_idle_timeout"`  // proxy idle exit duration (default "60m")
}

// Store bundles vault paths and provides IO helpers.
type Store struct {
	Root string
	// TokenPathOverride, when set, replaces the live token-file location
	// (used by tests and exotic setups).
	TokenPathOverride string
}

func DefaultRoot() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "agy-rotator"), nil
}

func Open() (*Store, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	s := &Store{Root: root}
	for _, d := range []string{root, s.AccountsDir(), filepath.Dir(s.TokenPath())} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) AccountsDir() string    { return filepath.Join(s.Root, "accounts") }
func (s *Store) statePath() string      { return filepath.Join(s.Root, "state.json") }
func (s *Store) configPath() string     { return filepath.Join(s.Root, "config.json") }
func (s *Store) quotaCachePath() string { return filepath.Join(s.Root, "quota-cache.json") }
func (s *Store) historyPath() string    { return filepath.Join(s.Root, "history.jsonl") }
func (s *Store) CAPath() string         { return filepath.Join(s.Root, "ca.pem") }
func (s *Store) CAKeyPath() string      { return filepath.Join(s.Root, "ca.key") }
func (s *Store) ProxyPIDPath() string   { return filepath.Join(s.Root, "proxy.pid") }
func (s *Store) ProxyLogPath() string   { return filepath.Join(s.Root, "proxy.log") }

// TokenPath is agy's live credential file.
func (s *Store) TokenPath() string {
	if s.TokenPathOverride != "" {
		return s.TokenPathOverride
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

// AgyBinPath resolves the real agy binary (env override wins).
func AgyBinPath() string {
	if p := os.Getenv("AGY_BIN"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "bin", "agy")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if lp, err := exec.LookPath("agy"); err == nil {
		return lp
	}
	return p
}

// LogDir is where agy writes its rotating CLI logs.
func LogDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "log")
}

// ---- generic atomic write ----

func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-agyrot-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), mode)
}

// ---- accounts ----

func slug(email string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(email)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func (s *Store) accountPath(email string) string {
	return filepath.Join(s.AccountsDir(), slug(email)+".json")
}

func (s *Store) SaveAccount(a *Account) error {
	if a.Email == "" {
		return errors.New("account email required")
	}
	if a.AddedAt.IsZero() {
		a.AddedAt = time.Now().UTC()
	}
	return writeJSON(s.accountPath(a.Email), a, 0o600)
}

func (s *Store) GetAccount(email string) (*Account, error) {
	b, err := os.ReadFile(s.accountPath(email))
	if err != nil {
		return nil, err
	}
	var a Account
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("corrupt account file %s: %w", email, err)
	}
	return &a, nil
}

func (s *Store) RemoveAccount(email string) error {
	err := os.Remove(s.accountPath(email))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ListAccounts returns accounts ordered by AddedAt then Email.
func (s *Store) ListAccounts() ([]*Account, error) {
	entries, err := os.ReadDir(s.AccountsDir())
	if err != nil {
		return nil, err
	}
	var out []*Account
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.AccountsDir(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var a Account
		if err := json.Unmarshal(b, &a); err != nil || a.Email == "" {
			continue // skip unreadable strays rather than fail everything
		}
		out = append(out, &a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AddedAt.Equal(out[j].AddedAt) {
			return out[i].AddedAt.Before(out[j].AddedAt)
		}
		return out[i].Email < out[j].Email
	})
	return out, nil
}

// ---- state ----

func (s *Store) LoadState() *State {
	st := &State{Accounts: map[string]AccountState{}}
	if b, err := os.ReadFile(s.statePath()); err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Accounts == nil {
		st.Accounts = map[string]AccountState{}
	}
	return st
}

func (s *Store) SaveState(st *State) error {
	return writeJSON(s.statePath(), st, 0o600)
}

// ---- config ----

func (s *Store) LoadConfig() Config {
	c := Config{
		Mode:              "round-robin",
		PreemptThreshold:  0.10,
		QuotaPollInterval: "5m",
		Notifications:     true,
		ProxyEnabled:      true,
		ProxyPort:         8999,
		ProxyIdleTimeout:  "60m",
	}
	if b, err := os.ReadFile(s.configPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.Mode != "round-robin" && c.Mode != "sticky" && c.Mode != "smart" {
		c.Mode = "round-robin"
	}
	if c.PreemptThreshold <= 0 || c.PreemptThreshold > 1 {
		c.PreemptThreshold = 0.10
	}
	if c.QuotaPollInterval == "" {
		c.QuotaPollInterval = "5m"
	}
	if c.ProxyPort <= 0 || c.ProxyPort > 65535 {
		c.ProxyPort = 8999
	}
	if c.ProxyIdleTimeout == "" {
		c.ProxyIdleTimeout = "60m"
	}
	return c
}

func (s *Store) SaveConfig(c Config) error {
	return writeJSON(s.configPath(), c, 0o600)
}

// ---- live token file ----

// ReadTokenFile reads agy's live credential file.
func (s *Store) ReadTokenFile() (*TokenFile, error) {
	b, err := os.ReadFile(s.TokenPath())
	if err != nil {
		return nil, err
	}
	var tf TokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return nil, fmt.Errorf("corrupt token file: %w", err)
	}
	return &tf, nil
}

// WriteTokenFile atomically installs tf as the live credential file.
func (s *Store) WriteTokenFile(tf *TokenFile) error {
	if tf.AuthMethod == "" {
		tf.AuthMethod = "consumer"
	}
	return writeJSON(s.TokenPath(), tf, 0o600)
}

// TokenFileExists reports whether the live credential file is present.
func (s *Store) TokenFileExists() bool {
	_, err := os.Stat(s.TokenPath())
	return err == nil
}

// ---- quota cache ----

type QuotaGroupCache struct {
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time,omitempty"`
	ModelCount        int     `json:"model_count"`
}

type AccountQuotaCache struct {
	Groups   map[string]QuotaGroupCache `json:"groups,omitempty"`
	Error    string                     `json:"error,omitempty"`
	CachedAt time.Time                  `json:"cached_at"`
}

type QuotaCache struct {
	FetchedAt time.Time                    `json:"fetched_at"`
	Accounts  map[string]AccountQuotaCache `json:"accounts"`
}

func (s *Store) LoadQuotaCache() *QuotaCache {
	qc := &QuotaCache{Accounts: map[string]AccountQuotaCache{}}
	if b, err := os.ReadFile(s.quotaCachePath()); err == nil {
		_ = json.Unmarshal(b, qc)
	}
	if qc.Accounts == nil {
		qc.Accounts = map[string]AccountQuotaCache{}
	}
	return qc
}

func (s *Store) SaveQuotaCache(qc *QuotaCache) error {
	if qc.FetchedAt.IsZero() {
		qc.FetchedAt = time.Now().UTC()
	}
	return writeJSON(s.quotaCachePath(), qc, 0o600)
}
