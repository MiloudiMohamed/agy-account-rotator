// Package detect classifies provider failure text into rotation reasons and
// computes cooldown backoffs. Port of the classification used by
// opencode-antigravity-auth, adapted for log/hook text instead of HTTP pairs.
package detect

import (
	"math/rand"
	"strings"
	"time"
)

type Reason string

const (
	QuotaExhausted Reason = "QUOTA_EXHAUSTED"
	RateLimited    Reason = "RATE_LIMIT_EXCEEDED"
	Capacity       Reason = "MODEL_CAPACITY_EXHAUSTED"
	Server         Reason = "SERVER_ERROR"
	AuthDead       Reason = "AUTH_REVOKED"
	Unknown        Reason = "UNKNOWN"
	None           Reason = "" // not a rotatable failure
)

var quotaBackoffs = []time.Duration{
	60 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
}

const (
	rateLimitBackoff = 30 * time.Second
	capacityBase     = 45 * time.Second
	capacityJitter   = 30 * time.Second // ±15s around base
	serverBackoff    = 20 * time.Second
	unknownBackoff   = 60 * time.Second
	minBackoff       = 2 * time.Second
)

// Classify maps free-form error text to a Reason. Empty text yields None.
func Classify(text string) Reason {
	l := strings.ToLower(text)
	if l == "" {
		return None
	}
	switch {
	case strings.Contains(l, "invalid_grant"), strings.Contains(l, "expired or revoked"),
		strings.Contains(l, "unauthenticated"), strings.Contains(l, "not logged into antigravity"):
		return AuthDead
	case strings.Contains(l, "capacity"), strings.Contains(l, "overloaded"),
		strings.Contains(l, "529"), strings.Contains(l, "model_capacity"):
		return Capacity
	case strings.Contains(l, "resource") && strings.Contains(l, "exhausted"):
		// gRPC ResourceExhausted — treated as transient capacity pressure,
		// matching opencode-antigravity-auth semantics
		return Capacity
	case strings.Contains(l, "per minute"), strings.Contains(l, "rate limit"),
		strings.Contains(l, "ratelimit"), strings.Contains(l, "too many requests"),
		strings.Contains(l, "429"):
		return RateLimited
	case strings.Contains(l, "quota"), strings.Contains(l, "exhausted"):
		return QuotaExhausted
	case strings.Contains(l, "internal error"), strings.Contains(l, "backend error"),
		strings.Contains(l, "500 "), strings.Contains(l, "503 "):
		return Server
	default:
		return None
	}
}

// BackoffFor returns how long an account should cool down after the given
// reason, given its consecutive failure count (0-based).
func BackoffFor(r Reason, consecutiveFailures int) time.Duration {
	switch r {
	case AuthDead:
		return 24 * time.Hour
	case QuotaExhausted:
		i := consecutiveFailures
		if i >= len(quotaBackoffs) {
			i = len(quotaBackoffs) - 1
		}
		return quotaBackoffs[i]
	case RateLimited:
		return rateLimitBackoff
	case Capacity:
		return capacityBase + time.Duration(rand.Int63n(int64(capacityJitter))) - capacityJitter/2
	case Server:
		return serverBackoff
	default:
		return unknownBackoff
	}
}
