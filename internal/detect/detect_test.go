package detect

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Reason
	}{
		{"", None},
		{"request failed: model is overloaded, try later", Capacity},
		{"rpc error: code = ResourceExhausted desc = Resource has been exhausted", Capacity},
		{"529 site is overloaded", Capacity},
		{"429 too many requests", RateLimited},
		{"generate requests per minute limit reached", RateLimited},
		{"rate limit exceeded for project", RateLimited},
		{"daily quota exceeded for gemini-3-pro", QuotaExhausted},
		{"quota exceeded", QuotaExhausted},
		{"oauth2: \"invalid_grant\" \"Bad Request\"", AuthDead},
		{"token refresh failed: token has been expired or revoked", AuthDead},
		{"Singleflight refresh failed: You are not logged into Antigravity.", AuthDead},
		{"internal error", Server},
		{"backend error 500 while streaming", Server},
		{"totally unrelated log line about git checkout", None},
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBackoffForLadder(t *testing.T) {
	if got := BackoffFor(AuthDead, 0); got != 24*time.Hour {
		t.Errorf("auth dead backoff = %v, want 24h", got)
	}
	if got := BackoffFor(QuotaExhausted, 0); got != 60*time.Second {
		t.Errorf("first quota backoff = %v, want 60s", got)
	}
	if got := BackoffFor(QuotaExhausted, 3); got != 2*time.Hour {
		t.Errorf("fourth quota backoff = %v, want 2h", got)
	}
	if got := BackoffFor(QuotaExhausted, 99); got != 2*time.Hour {
		t.Errorf("clamped quota backoff = %v, want 2h", got)
	}
	if got := BackoffFor(RateLimited, 0); got != 30*time.Second {
		t.Errorf("rate-limit backoff = %v, want 30s", got)
	}
	if got := BackoffFor(Server, 0); got != 20*time.Second {
		t.Errorf("server backoff = %v, want 20s", got)
	}
	// capacity: base ± jitter/2
	got := BackoffFor(Capacity, 0)
	if got < capacityBase-15*time.Second || got > capacityBase+15*time.Second {
		t.Errorf("capacity backoff %v outside ±15s band around %v", got, capacityBase)
	}
	if got := BackoffFor(Unknown, 2); got != unknownBackoff {
		t.Errorf("unknown backoff = %v, want %v", got, unknownBackoff)
	}
}
