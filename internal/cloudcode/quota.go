// Package cloudcode integrates with Google's Cloud Code Assist endpoints to
// inspect live per-model quota buckets and availability.
package cloudcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var fetchModelsEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"

const (
	antigravityUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Antigravity/1.1.20 Chrome/138.0.7204.235 Electron/37.3.1 Safari/537.36"
	googApiClient = "google-cloud-sdk vscode_cloudshelleditor/0.1"
)

type QuotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	ResetTime         string   `json:"resetTime,omitempty"`
}

type ModelEntry struct {
	DisplayName string     `json:"displayName,omitempty"`
	ModelName   string     `json:"modelName,omitempty"`
	QuotaInfo   *QuotaInfo `json:"quotaInfo,omitempty"`
}

type ModelsResponse struct {
	Models map[string]ModelEntry `json:"models"`
}

type GroupSummary struct {
	Group             string  `json:"group"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time,omitempty"`
	ModelCount        int     `json:"model_count"`
}

type AccountQuota struct {
	Email    string         `json:"email"`
	Active   bool           `json:"active,omitempty"`
	Groups   []GroupSummary `json:"groups,omitempty"`
	Error    string         `json:"error,omitempty"`
	Failures int            `json:"failures,omitempty"`
	Cooling  string         `json:"cooling,omitempty"`
}

// DefaultProjectID returns the cached project hint from agy's data directory if present.
func DefaultProjectID() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "default_project_id.txt")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// FetchAvailableModels queries Cloud Code Assist for live quota per model.
func FetchAvailableModels(ctx context.Context, client *http.Client, accessToken, projectID string) (*ModelsResponse, error) {
	bodyData := map[string]string{}
	if projectID != "" {
		bodyData["project"] = projectID
	}
	b, _ := json.Marshal(bodyData)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchModelsEndpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityUA)
	req.Header.Set("X-Goog-Api-Client", googApiClient)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		if errResp.Error.Message != "" {
			return nil, fmt.Errorf("status %d (%s): %s", resp.StatusCode, errResp.Error.Status, errResp.Error.Message)
		}
		return nil, fmt.Errorf("status %s", resp.Status)
	}

	var data ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding quota response: %w", err)
	}
	return &data, nil
}

// ClassifyGroup returns "claude", "gemini-pro", or "gemini-flash" for model strings.
func ClassifyGroup(modelName, displayName string) string {
	combined := strings.ToLower(modelName + " " + displayName)
	if strings.Contains(combined, "claude") {
		return "claude"
	}
	if strings.Contains(combined, "gemini-3") || strings.Contains(combined, "gemini 3") || strings.Contains(combined, "gemini-2.5") {
		if strings.Contains(combined, "flash") {
			return "gemini-flash"
		}
		return "gemini-pro"
	}
	return ""
}

func NormalizeFraction(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0
	}
	if *v < 0 {
		return 0
	}
	if *v > 1 {
		return 1
	}
	return *v
}

func ParseReset(tStr string) (time.Time, bool) {
	if tStr == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, tStr)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, tStr)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// GroupModels aggregates per-model entries into canonical groups.
// Ported from chrisgeo: representative model has highest remaining fraction,
// ties broken by earliest parseable reset time.
func GroupModels(resp *ModelsResponse) []GroupSummary {
	if resp == nil || len(resp.Models) == 0 {
		return nil
	}

	type cand struct {
		fraction   float64
		resetTime  string
		resetT     time.Time
		modelCount int
		hasReset   bool
		seen       bool
	}
	grouped := map[string]*cand{}

	for modelName, entry := range resp.Models {
		grp := ClassifyGroup(modelName, entry.DisplayName)
		if grp == "" {
			continue
		}

		c, exists := grouped[grp]
		if !exists {
			c = &cand{}
			grouped[grp] = c
		}
		c.modelCount++

		var fraction float64
		var resetStr string
		var resetT time.Time
		var hasReset bool

		if entry.QuotaInfo != nil {
			fraction = NormalizeFraction(entry.QuotaInfo.RemainingFraction)
			resetStr = entry.QuotaInfo.ResetTime
			if t, ok := ParseReset(resetStr); ok {
				resetT = t
				hasReset = true
			}
		}

		if !c.seen {
			c.seen = true
			c.fraction = fraction
			c.resetTime = resetStr
			c.resetT = resetT
			c.hasReset = hasReset
			continue
		}

		if fraction > c.fraction {
			c.fraction = fraction
			c.resetTime = resetStr
			c.resetT = resetT
			c.hasReset = hasReset
		} else if fraction == c.fraction && hasReset {
			if !c.hasReset || resetT.Before(c.resetT) {
				c.resetTime = resetStr
				c.resetT = resetT
				c.hasReset = true
			}
		}
	}

	order := []string{"claude", "gemini-pro", "gemini-flash"}
	var out []GroupSummary
	for _, g := range order {
		if c, ok := grouped[g]; ok && c.seen {
			out = append(out, GroupSummary{
				Group:             g,
				RemainingFraction: c.fraction,
				ResetTime:         c.resetTime,
				ModelCount:        c.modelCount,
			})
		}
	}
	return out
}

// ProgressBar renders a 10-character meter like "████████░░".
func ProgressBar(fraction float64) string {
	const totalBlocks = 10
	fraction = math.Max(0, math.Min(1, fraction))
	filled := int(math.Round(fraction * totalBlocks))
	if filled > totalBlocks {
		filled = totalBlocks
	}
	empty := totalBlocks - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// FormatReset turns a future reset timestamp into a human delta like "resets in 3h12m".
func FormatReset(resetStr string, now time.Time) string {
	t, ok := ParseReset(resetStr)
	if !ok {
		return ""
	}
	if t.Before(now) {
		return "resets soon"
	}
	d := t.Sub(now).Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("resets in %dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("resets in %dm", m)
	}
	return fmt.Sprintf("resets in %ds", int(d.Seconds()))
}

// SortByGroupOrder ensures stable group sorting.
func SortGroupSummaries(groups []GroupSummary) {
	rank := map[string]int{"claude": 1, "gemini-pro": 2, "gemini-flash": 3}
	sort.SliceStable(groups, func(i, j int) bool {
		return rank[groups[i].Group] < rank[groups[j].Group]
	})
}
