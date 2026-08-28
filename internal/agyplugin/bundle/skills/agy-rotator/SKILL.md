---
name: agy-rotator
description: Manage multi-account rotation for the Antigravity CLI. Use when the user asks about account rotation, quota switching, which account is active, or mentions 'agy-rotator', 'rotate account', or 'switch account'.
---

# agy-rotator

This machine runs `agy` through an account-rotation shim. Multiple Google
accounts are vaulted and rotated automatically on each launch; rate-limited
accounts enter cooldown instead of being reused.

## Key facts

- Rotation applies automatically on launch and during active requests via the transparent in-flight proxy on `127.0.0.1:8999`.
- Mid-session 429 rate limits are automatically caught and retried on the next healthy account without dropping your session.
- Vault location: `~/.local/share/agy-rotator/`

## Commands

| Command | Purpose |
|---|---|
| `agy-rotator status` | Show active account, all vaulted accounts, cooldowns |
| `agy-rotator list` | List vaulted accounts |
| `agy-rotator quota` | Preview live remaining quota per model for all accounts |
| `agy-rotator stats` | Show conversation counts, steps, and local usage stats |
| `agy-rotator why` | Explain why current account is active and selection state |
| `agy-rotator history` | Audit log of rotations, cooldowns, and failures |
| `agy-rotator statusline` | Compact terminal status-bar segment (0ms, cached) |
| `agy-rotator proxy` | Inspect or manage in-flight transparent request proxy |
| `agy-rotator rotate` | Switch live credentials to the next account now |
| `agy-rotator watch` | Tail logs; auto-cooldown + rotate on quota errors |
| `agy-rotator doctor` | Re-validate stored refresh tokens against Google |
| `agy-rotator export` | Export encrypted vault envelope |
| `agy-rotator import` | Import accounts from encrypted vault envelope |
| `agy-rotator config` | View or update configuration settings |
| `agy-rotator add` | Capture another Google account via browser link |

When the user asks which account they are on, run `agy-rotator status` and
report the `active:` line.
