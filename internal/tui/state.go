package tui

import (
	"math"
	"strings"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
)

// accountRowState identifies the single display state of an account row (Section 7).
type accountRowState int

const (
	stateMetersOff accountRowState = iota
	stateAPIKey
	stateReading
	stateNoReading
	stateLoginExpired
	stateRateLimited
	stateOffline
	stateError
	stateOutdated
	stateHealthy
	stateRunningLow
	stateExhausted
)

// outdatedAge is twice the usage cache TTL (2 * 180s = 6m).
const outdatedAge = 2 * config.UsageTTL

// windowView holds display-ready values for one quota window.
type windowView struct {
	label     string
	leftPct   float64       // 0..100
	timeLeft  float64       // 0..1, or -1 if unknown / no reading
	hasReset  bool          // true when reset duration is known and window started
	resetDur  time.Duration // positive duration until reset
	level     barLevel
	noReading bool
}

// isAPIKey reports whether an account entry represents an API-key slot (no usage).
func isAPIKey(acct provider.AccountEntry) bool {
	if strings.Contains(strings.ToLower(acct.Note), "api key") {
		return true
	}
	if acct.Plan != nil && (strings.EqualFold(acct.Plan.Label, "API") || strings.EqualFold(acct.Plan.Label, "API key")) {
		return true
	}
	return false
}

// planLabel returns the plan column text for an account ("API" for API-key slots).
func planLabel(acct provider.AccountEntry) string {
	if isAPIKey(acct) {
		return "API"
	}
	if acct.Plan != nil {
		return acct.Plan.Label
	}
	return ""
}

// usageAge returns the elapsed duration since the reading was fetched.
func usageAge(u *provider.UsageInfo, now time.Time) (time.Duration, bool) {
	if u == nil {
		return 0, false
	}
	if u.FetchedAt != nil && *u.FetchedAt > 0 {
		fetched := time.Unix(int64(*u.FetchedAt), 0)
		d := now.Sub(fetched)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	if u.AgeSeconds != nil {
		sec := *u.AgeSeconds
		if sec < 0 {
			sec = 0
		}
		return time.Duration(sec) * time.Second, true
	}
	return 0, false
}

func isAuthError(errText string) bool {
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "expired") ||
		strings.Contains(lower, "refresh") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401") ||
		strings.Contains(lower, "403")
}

// rowState determines the single display state for an account row.
func rowState(acct provider.AccountEntry, metersOn, loading bool, now time.Time) accountRowState {
	if !metersOn {
		return stateMetersOff
	}
	if isAPIKey(acct) {
		return stateAPIKey
	}
	u := acct.Usage
	if u == nil {
		if loading {
			return stateReading
		}
		return stateNoReading
	}
	if u.Error != nil && *u.Error != "" {
		errText := *u.Error
		lower := strings.ToLower(errText)
		switch {
		case isAuthError(errText):
			return stateLoginExpired
		case strings.Contains(lower, "rate limited"):
			return stateRateLimited
		case strings.Contains(lower, "offline"):
			return stateOffline
		default:
			return stateError
		}
	}
	if len(u.Windows) == 0 {
		if loading {
			return stateReading
		}
		return stateNoReading
	}
	if age, ok := usageAge(u, now); ok && age > outdatedAge {
		return stateOutdated
	}

	worst := barOK
	for _, w := range u.Windows {
		left := math.Max(0, math.Min(100, 100-w.Pct))
		lvl := barState(left)
		if lvl > worst {
			worst = lvl
		}
	}
	switch worst {
	case barBad:
		return stateExhausted
	case barWarn:
		return stateRunningLow
	default:
		return stateHealthy
	}
}

// buildWindowViews prepares display-ready window views for an account's usage data.
func buildWindowViews(u *provider.UsageInfo, now time.Time) []windowView {
	if u == nil || len(u.Windows) == 0 {
		return nil
	}
	out := make([]windowView, 0, len(u.Windows))
	for _, w := range u.Windows {
		left := math.Max(0, math.Min(100, 100-w.Pct))
		v := windowView{
			label:    w.Label,
			leftPct:  left,
			timeLeft: -1,
			level:    barState(left),
		}
		epoch, hasEpoch := provider.ResetEpoch(w.ResetsAt)
		if hasEpoch {
			rem := time.Duration((epoch - float64(now.Unix())) * float64(time.Second))
			if rem < 0 {
				rem = 0
			}
			v.hasReset = true
			v.resetDur = rem
			if winDur, ok := windowDuration(w.Label); ok && winDur > 0 {
				v.timeLeft = math.Max(0, math.Min(1, rem.Seconds()/winDur.Seconds()))
			}
		}
		out = append(out, v)
	}
	return out
}

// defaultNoReadingWindows returns the placeholder empty-track windows for "No reading yet".
func defaultNoReadingWindows() []windowView {
	return []windowView{
		{label: "5h", timeLeft: -1, noReading: true},
		{label: "7d", timeLeft: -1, noReading: true},
	}
}

// lowestWindow returns the window with the least quota remaining.
func lowestWindow(views []windowView) (windowView, bool) {
	if len(views) == 0 {
		return windowView{}, false
	}
	best := views[0]
	for _, v := range views[1:] {
		if v.leftPct < best.leftPct {
			best = v
		}
	}
	return best, true
}
