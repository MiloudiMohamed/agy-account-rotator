# agy-account-rotator

Seamless multi-account rotation for the [Antigravity CLI](https://antigravity.google) (`agy`).

Manage and rotate multiple Google accounts without repeatedly logging in and out. `agy-rotator` vaults your authorized Google accounts and switches between them across two layers: during active requests (in-flight auto-retry on 429 rate limits) and at process startup (pre-flight launch rotation). It tracks live model quotas, enforces cooldown ladders, and provides background monitoring.

---

## Features

- **In-Flight 429 Auto-Failover**: Intercepts model requests transparently, injects active credentials per request, and automatically retries on HTTP 429 rate limits or quota exhaustion without dropping your interactive session.
- **Dual-Layer Account Rotation**: Rotates accounts both in-flight during active requests and pre-flight upon every CLI launch or print-mode (`-p`) invocation.
- **Live Quota Inspector**: Real-time per-model quota preview (`claude`, `gemini-pro`, `gemini-flash`) with progress meters and reset countdowns.
- **Smart Quota-Aware Mode**: Routes requests and launches to whichever account currently maintains the highest remaining capacity.
- **Statusline Integration**: Zero-latency, cached terminal segment for status-bar scripts (shows active account and quota meters).
- **Background Watcher & Service**: Background daemon that monitors logs, enforces cooldown ladders, proactively rotates low-quota accounts, and sends desktop notifications.
- **Audit Trail & Decision Insights**: `why` explains why an account is active; `history` provides an audit log of all rotations and cooldown events.
- **Usage Analytics**: `stats` computes local conversation volume, session counts, step volume, and storage size.
- **Passphrase-Encrypted Vault**: Export and import vaulted accounts across systems using AES-256-GCM and PBKDF2 encryption.
- **Token Health & Self-Healing**: `doctor -fix` re-validates OAuth refresh tokens against Google and prunes permanently revoked accounts.
- **Shell Completions**: Native autocompletion for Zsh and Bash.

---

## Quickstart

### 1. Install

```bash
curl -fsSL https://raw.githubusercontent.com/MiloudiMohamed/agy-account-rotator/main/install.sh | bash
```

The installer automatically:
1. Downloads the appropriate binary for your OS and architecture (`~/.local/bin/agy-rotator`).
2. Configures the launch shim (`~/.agy-rotator/bin/agy`) and updates your shell rc.
3. Installs shell tab-completions and the agy agent skill bundle.

### 2. Add your Google accounts

```bash
agy-rotator add
```

```text
=== Adding account #1 ===
1. Open this link in your browser and approve access:
   https://accounts.google.com/o/oauth2/auth?client_id=1071006060591-...

2. After approving, you will land on a page that fails to load (that is normal).
   Copy the URL from your browser's address bar (it starts with https://antigravity.google/oauth-callback).

3. Paste the URL (or just the code) here: https://antigravity.google/oauth-callback?code=4%2F0ATs...
Captured and activated: you@gmail.com
Add another? [Y/n]
```

Repeat for each account. OAuth tokens are stored locally at `~/.local/share/agy-rotator/` with strict `0600` permissions.

### 3. Use `agy` as usual

Launch `agy` normally. Each session automatically selects the appropriate account:

```bash
agy
```

---

## Feature Guide

### Live Quota Preview

Inspect remaining quota across all vaulted accounts in real time:

```bash
agy-rotator quota
```

```text
alice@gmail.com
  claude         ██████████ 100%  resets in 5h00m
  gemini-pro     ██████░░░░  64%  resets in 3h06m
  gemini-flash   ██████░░░░  64%  resets in 3h06m

bob@gmail.com [active]
  claude         ██████████ 100%  resets in 5h00m
  gemini-pro     ██████░░░░  62%  resets in 3h48m
  gemini-flash   ██████░░░░  62%  resets in 3h48m
```

Target a specific account or output raw JSON:
```bash
agy-rotator quota -email you@gmail.com
agy-rotator quota -json
```

---

### Statusline Integration

`agy-rotator statusline` outputs a fast terminal segment reading cached state without network latency:

```text
alice C:100% P:73% F:73%
```

To embed it in your `~/.config/agy/statusline.sh`, add these lines:

```bash
rotator_seg=$(agy-rotator statusline 2>/dev/null || true)
[ -n "$rotator_seg" ] && left_section="${left_section}${COLOR_SEP}${rotator_seg}"
```

---

### Selection Modes & Smart Rotation

Configure how accounts are selected before each launch:

```bash
# Smart mode: selects account with highest remaining quota
agy-rotator set-mode smart

# Round-robin: cycles evenly across healthy accounts (default)
agy-rotator set-mode round-robin

# Sticky: stays on current account until rate-limited, then switches
agy-rotator set-mode sticky
```

---

### Background Watcher & Service

Run the watcher in the background to tail logs, enforce cooldown ladders, and proactively switch accounts:

```bash
# Install as a systemd user daemon (survives reboots)
agy-rotator watch install-service

# Check service status & logs
agy-rotator watch status-service

# Stop and uninstall the service
agy-rotator watch uninstall-service

# Or run interactively in a terminal
agy-rotator watch
```

---

### Vault Migration & Backups

Export and import your vaulted accounts across machines using **AES-256-GCM** encryption with PBKDF2 key derivation:

```bash
# Export encrypted vault backup
agy-rotator export -out vault.enc

# Import and decrypt on a new machine
agy-rotator import vault.enc

# Replace existing vault entirely instead of merging
agy-rotator import -replace vault.enc
```

---

### Decision Insights & Audit Log

```bash
# Explain current selection state and cooldown status
agy-rotator why

# View historical audit log of rotations and cooldown triggers
agy-rotator history -n 25
```

---

### Conversation Usage Analytics

Inspect local conversation storage, activity dates, and step volume:

```bash
agy-rotator stats
```

```text
Conversations:    19 (5 active today, 10 this week)
Total Steps:      1284
Storage Size:     17.9 MB
Activity Span:    2026-08-14 to 2026-08-26
Rotations Logged: 12 (1 cooldowns triggered)

Vault Accounts:
    alice@gmail.com              healthy (6 switches) — last used 2026-08-26 00:38
  * bob@gmail.com                healthy (6 switches) — last used 2026-08-26 00:38
```

---

### In-Flight Request Proxy & 429 Auto-Retry

The transparent local proxy runs on `127.0.0.1:8999` and intercepts model inference calls in flight:

```bash
# Check runtime status, PID, active account, and processed request count
agy-rotator proxy status

# Manually start or stop the background proxy daemon
agy-rotator proxy start
agy-rotator proxy stop

# View the local self-signed CA certificate paths
agy-rotator proxy cert
```

- **Per-Request Token Injection**: Swaps credentials on every model generation call to maintain active account state.
- **Mid-Session 429 Failover**: When Google returns a 429 rate limit or quota exhaustion error, the proxy automatically moves the current account into cooldown, selects the next healthy account, and replays the request seamlessly.
- **Zero-Touch Lifecycle**: The launch shim auto-spawns the background proxy on demand, and the proxy automatically shuts down after 60 minutes of inactivity.

---

### Dynamic Configuration

View or update settings without editing raw configuration files:

```bash
# View all settings
agy-rotator config

# Read or change specific keys
agy-rotator config get mode
agy-rotator config set mode smart
agy-rotator config set preempt_threshold 0.15
agy-rotator config set quota_poll_interval 10m
agy-rotator config set notifications true
agy-rotator config set proxy_port 8999
agy-rotator config set proxy_idle_timeout 60m
```

---

## Commands Reference

| Command | Description |
|---|---|
| `agy-rotator add [-label L]` | Capture account(s) via browser sign-in links |
| `agy-rotator list` | List all vaulted accounts |
| `agy-rotator status` | View active account and current cooldown status |
| `agy-rotator quota [-email E]` | Preview live remaining quota per model for all accounts |
| `agy-rotator stats` | Show conversation volume, steps, and local usage metrics |
| `agy-rotator why` | Explain why the current account is active & selection state |
| `agy-rotator history [-n N]` | View audit log of rotations, cooldowns, and failures |
| `agy-rotator statusline [--no-color]` | Output fast terminal status-bar segment (0ms, cached) |
| `agy-rotator proxy [start\|stop\|status]` | Manage in-flight transparent request proxy (auto-spawned on launch) |
| `agy-rotator rotate` | Switch live credentials to the next account immediately |
| `agy-rotator use -email E` | Activate a specific account manually |
| `agy-rotator remove -email E` | Remove an account from the vault |
| `agy-rotator doctor [-email E] [-fix]` | Re-validate tokens (use `-fix` to prune revoked accounts) |
| `agy-rotator export [-out file]` | Export passphrase-encrypted vault envelope |
| `agy-rotator import [-replace] [file]` | Import accounts from encrypted vault envelope |
| `agy-rotator config [get\|set]` | View or update configuration settings |
| `agy-rotator set-mode <mode>` | Set selection strategy (`round-robin`, `sticky`, `smart`) |
| `agy-rotator watch [install-service]` | Run or install background daemon for auto-cooldowns |
| `agy-rotator plugin install` | Install agy-native plugin skill bundle |
| `agy-rotator completions install` | Install shell tab-completions (Zsh / Bash) |
| `agy-rotator shim install [--write-rc]` | Manage PATH launch shim |

**Escape Hatches**:
- Run `AGY_ROTATOR_DISABLE=1 agy` to bypass rotation completely for a single execution.
- Run `AGY_ROTATOR_NO_PROXY=1 agy` to disable the in-flight transparent proxy.

---

## Architecture & How It Works

Antigravity CLI reads its active OAuth credentials from `~/.gemini/antigravity-cli/antigravity-oauth-token` at process startup and routes model requests to Google APIs. `agy-rotator` manages multi-account rotation across two layers:

1. **At Process Startup (Launch Shim)**: Swaps the live credential file atomically before `agy` executes and ensures the background proxy is running.
2. **During Active Requests (In-Flight Proxy)**: Intercepts outgoing requests to `cloudcode-pa.googleapis.com` on `127.0.0.1:8999`. If Google returns an HTTP 429 rate limit or quota limit, the proxy automatically marks the account in cooldown, selects the next healthy account, injects its Bearer token, and retries the request seamlessly.

- **OAuth Authentication**: Uses agy's public installed-app client with PKCE (RFC 7636). Captured credentials never pass through third-party servers.
- **Cooldown Ladder**: When rate limits or quotas are hit, accounts enter escalating cooldowns (60s → 5m → 30m → 2h), skipping them during rotation until refreshed.
- **Zero-Touch Automation**: The proxy is auto-spawned by the launch shim and terminates when idle. No manual daemon management is required.

---

## Uninstallation

```bash
# 1. Remove PATH shim
agy-rotator shim uninstall

# 2. Stop and remove background service (if installed)
agy-rotator watch uninstall-service 2>/dev/null || true

# 3. Remove binary and vault directories
rm "$(command -v agy-rotator)"
rm -rf ~/.local/share/agy-rotator ~/.agy-rotator

# 4. Remove the '# added by agy-rotator' line from ~/.zshrc or ~/.bashrc
```

---

## Development

```bash
make build   # Build binary to bin/agy-rotator
make test    # Run full unit test suite
make vet     # Run go vet
make fmt     # Format code
make install # Install locally to ~/.local/bin/agy-rotator
```

Releases are automatically built and published via GitHub Actions when pushing a git tag:
```bash
git tag v0.1.0 && git push origin v0.1.0
```

---

## License

MIT
