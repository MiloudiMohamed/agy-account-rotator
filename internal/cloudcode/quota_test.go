package cloudcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func floatPtr(v float64) *float64 { return &v }

func TestClassifyGroup(t *testing.T) {
	cases := []struct {
		model string
		disp  string
		want  string
	}{
		{"claude-3-7-sonnet", "Claude 3.7 Sonnet", "claude"},
		{"claude-opus-4-6-thinking", "Claude Opus 4.6", "claude"},
		{"gemini-3-pro", "Gemini 3 Pro (High)", "gemini-pro"},
		{"gemini-3-flash", "Gemini 3 Flash", "gemini-flash"},
		{"gemini-2.5-pro", "Gemini 2.5 Pro", "gemini-pro"},
		{"gemini-2.5-flash", "Gemini 2.5 Flash", "gemini-flash"},
		{"unrelated-gpt4", "GPT-4", ""},
	}
	for _, c := range cases {
		if got := ClassifyGroup(c.model, c.disp); got != c.want {
			t.Errorf("ClassifyGroup(%q, %q) = %q, want %q", c.model, c.disp, got, c.want)
		}
	}
}

func TestNormalizeFraction(t *testing.T) {
	if got := NormalizeFraction(nil); got != 0 {
		t.Errorf("nil = %v, want 0", got)
	}
	if got := NormalizeFraction(floatPtr(-0.5)); got != 0 {
		t.Errorf("-0.5 = %v, want 0", got)
	}
	if got := NormalizeFraction(floatPtr(1.5)); got != 1 {
		t.Errorf("1.5 = %v, want 1", got)
	}
	if got := NormalizeFraction(floatPtr(0.75)); got != 0.75 {
		t.Errorf("0.75 = %v, want 0.75", got)
	}
}

func TestGroupModels(t *testing.T) {
	t1 := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	t2 := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)

	resp := &ModelsResponse{
		Models: map[string]ModelEntry{
			"claude-sonnet": {
				DisplayName: "Claude Sonnet",
				QuotaInfo:   &QuotaInfo{RemainingFraction: floatPtr(0.4), ResetTime: t1},
			},
			"claude-opus": {
				DisplayName: "Claude Opus",
				QuotaInfo:   &QuotaInfo{RemainingFraction: floatPtr(0.8), ResetTime: t2},
			},
			"gemini-3-flash": {
				DisplayName: "Gemini 3 Flash",
				QuotaInfo:   &QuotaInfo{RemainingFraction: floatPtr(1.0)},
			},
		},
	}

	groups := GroupModels(resp)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// Claude group must pick higher fraction (0.8) and its coupled reset time (t2)
	claude := groups[0]
	if claude.Group != "claude" || claude.RemainingFraction != 0.8 || claude.ResetTime != t2 || claude.ModelCount != 2 {
		t.Errorf("claude group mismatch: %+v", claude)
	}

	flash := groups[1]
	if flash.Group != "gemini-flash" || flash.RemainingFraction != 1.0 || flash.ModelCount != 1 {
		t.Errorf("flash group mismatch: %+v", flash)
	}
}

func TestProgressBar(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.0, "░░░░░░░░░░"},
		{0.5, "█████░░░░░"},
		{1.0, "██████████"},
		{0.78, "████████░░"},
	}
	for _, c := range cases {
		if got := ProgressBar(c.in); got != c.want {
			t.Errorf("ProgressBar(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatReset(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(2*time.Hour + 30*time.Minute).Format(time.RFC3339)
	if got := FormatReset(future, now); got != "resets in 2h30m" {
		t.Errorf("FormatReset = %q, want 'resets in 2h30m'", got)
	}
	past := now.Add(-time.Minute).Format(time.RFC3339)
	if got := FormatReset(past, now); got != "resets soon" {
		t.Errorf("past reset = %q, want 'resets soon'", got)
	}
	if got := FormatReset("", now); got != "" {
		t.Errorf("empty reset = %q, want ''", got)
	}
}

func TestFetchAvailableModels(t *testing.T) {
	var gotAuth, gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"models": {
				"gemini-3-pro": {
					"displayName": "Gemini 3 Pro",
					"quotaInfo": { "remainingFraction": 0.85 }
				}
			}
		}`))
	}))
	defer srv.Close()

	old := fetchModelsEndpoint
	fetchModelsEndpoint = srv.URL
	defer func() { fetchModelsEndpoint = old }()

	resp, err := FetchAvailableModels(context.Background(), srv.Client(), "token-123", "proj-abc")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer token-123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	_ = gotProject
	if len(resp.Models) != 1 || resp.Models["gemini-3-pro"].DisplayName != "Gemini 3 Pro" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
