package tui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Anurag-Raut/tori/internal/provider"
	"github.com/Anurag-Raut/tori/internal/store"
)

var updateGolden = flag.Bool("update", false, "update golden snapshot files")

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func makeJWT(payload map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(payload)
	pl := base64.RawURLEncoding.EncodeToString(b)
	return hdr + "." + pl + ".sig"
}

func fixedNow() time.Time {
	return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
}

func setupTestEnv(t *testing.T, rt http.RoundTripper) (*provider.Env, map[string]string) {
	t.Helper()
	prevClock, prevUsage, prevSucc, prevErr := clockTickInterval, usageTickInterval, successMsgTTL, errorMsgTTL
	clockTickInterval = time.Millisecond
	usageTickInterval = time.Millisecond
	successMsgTTL = time.Millisecond
	errorMsgTTL = time.Millisecond
	t.Cleanup(func() {
		clockTickInterval = prevClock
		usageTickInterval = prevUsage
		successMsgTTL = prevSucc
		errorMsgTTL = prevErr
	})

	home := t.TempDir()
	envMap := map[string]string{}
	now := fixedNow()
	env := &provider.Env{
		Root:       filepath.Join(home, ".tori"),
		Home:       home,
		HTTPClient: &http.Client{Transport: rt, Timeout: 5 * time.Second},
		Now:        func() time.Time { return now },
		GOOS:       "linux",
		GOARCH:     "amd64",
		ASCII:      true,
		Environ:    func(k string) string { return envMap[k] },
	}
	return env, envMap
}

func writeCodexSlot(t *testing.T, env *provider.Env, id, email, plan, alias string, setLive bool) {
	t.Helper()
	if plan == "" {
		plan = "plus"
	}
	auth := map[string]any{
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.Now().Unix() + 3600}),
			"refresh_token": "rt-" + id,
			"account_id":    id,
			"id_token": makeJWT(map[string]any{
				"email":                       email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
			}),
		},
	}
	if setLive {
		_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".codex", "auth.json"), auth, 0o600, false)
	}
	x := &provider.Codex{}
	var args []string
	if alias != "" {
		args = []string{alias}
	}
	if setLive {
		if _, err := x.Add(env, args); err != nil {
			t.Fatal(err)
		}
	}
}

func writeClaudeSlot(t *testing.T, env *provider.Env, uid, email, plan, alias string, setLive bool) {
	t.Helper()
	if plan == "" {
		plan = "pro"
	}
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-" + uid,
			"refreshToken":     "rt-" + uid,
			"expiresAt":        float64(env.Now().Unix()+3600) * 1000,
			"subscriptionType": plan,
		},
	}
	cfg := map[string]any{
		"oauthAccount": map[string]any{
			"accountUuid":  uid,
			"emailAddress": email,
		},
	}
	if setLive {
		_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude", ".credentials.json"), creds, 0o600, false)
		_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude.json"), cfg, 0o600, false)
	}
	c := &provider.Claude{}
	var args []string
	if alias != "" {
		args = []string{alias}
	}
	if setLive {
		if _, err := c.Add(env, args); err != nil {
			t.Fatal(err)
		}
	}
}

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func keyType(kt tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: kt}
}

func execCmd(m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	if cmd == nil {
		return m, nil
	}
	msg := cmd()
	if msg == nil {
		return m, nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var lastCmd tea.Cmd
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			subMsg := sub()
			if subMsg == nil {
				continue
			}
			switch subMsg.(type) {
			case spinner.TickMsg, clockTickMsg, usageTickMsg, clearHeaderMsg:
				continue
			}
			next, c := m.Update(subMsg)
			m = next.(Model)
			lastCmd = c
		}
		return m, lastCmd
	}
	switch msg.(type) {
	case spinner.TickMsg, clockTickMsg, usageTickMsg, clearHeaderMsg:
		return m, nil
	}
	next, nextCmd := m.Update(msg)
	return next.(Model), nextCmd
}

func ptrStr(s string) *string     { return &s }
func ptrInt(n int) *int           { return &n }
func ptrFloat(f float64) *float64 { return &f }

func withASCIIProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prev)
	})
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	// Ensure pure ASCII in golden snapshots (no stray non-ASCII bytes or ANSI escapes).
	if strings.Contains(got, "\x1b") {
		t.Fatalf("golden %s contains ANSI escape sequences:\n%q", name, got)
	}
	for i := 0; i < len(got); i++ {
		if got[i] > 127 {
			t.Fatalf("golden %s contains non-ASCII byte 0x%x at offset %d:\n%s", name, got[i], i, got)
		}
	}

	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update to create): %v", path, err)
	}
	want := strings.ReplaceAll(string(wantBytes), "\r\n", "\n")
	if got != want {
		t.Fatalf("golden mismatch for %s\n--- WANT ---\n%s\n--- GOT ---\n%s", name, want, got)
	}
}

// buildFixtureModel builds a deterministic ASCII Model for golden snapshot tests.
func buildFixtureModel(t *testing.T, width, height int) Model {
	t.Helper()
	now := fixedNow()
	fetched := float64(now.Add(-30 * time.Second).Unix())
	env, envMap := setupTestEnv(t, nil)
	env.DisableNetwork = true
	envMap["TORI_NO_ANIM"] = "1"

	m := NewModel(env)
	m.loading = false
	m.usageOn = map[string]bool{
		"claude":      true,
		"codex":       false,
		"antigravity": false,
	}
	m.data = map[string][]provider.AccountEntry{
		"claude": {
			{
				Number: 1,
				Email:  "alex.chen.86@example.com",
				Alias:  ptrStr("Personal"),
				Active: true,
				Plan:   &provider.PlanInfo{Label: "Pro"},
				Usage: &provider.UsageInfo{
					FetchedAt:  ptrFloat(fetched),
					AgeSeconds: ptrInt(30),
					Windows: []provider.Window{
						{Label: "5h", Pct: 0, ResetsAt: nil}, // window not started
						{Label: "7d", Pct: 22, ResetsAt: float64(now.Add(5*24*time.Hour + 10*time.Hour).Unix())},
					},
				},
			},
			{
				Number: 2,
				Email:  "devwork@example.com",
				Alias:  ptrStr("Work"),
				Active: false,
				Plan:   &provider.PlanInfo{Label: "Pro"},
				Usage: &provider.UsageInfo{
					FetchedAt:  ptrFloat(fetched),
					AgeSeconds: ptrInt(30),
					Windows: []provider.Window{
						{Label: "5h", Pct: 0, ResetsAt: nil},
						{Label: "7d", Pct: 37, ResetsAt: float64(now.Add(4*24*time.Hour + 11*time.Hour).Unix())},
					},
				},
			},
		},
		"codex": {
			{
				Number: 1,
				Email:  "devwork@example.com",
				Alias:  ptrStr("Work"),
				Active: true,
				Plan:   &provider.PlanInfo{Label: "Free"},
			},
		},
		"antigravity": {
			{
				Number: 1,
				Email:  "other@example.com",
				Active: false,
			},
			{
				Number: 2,
				Email:  "alex.chen.86@example.com",
				Active: true,
			},
		},
	}
	m.selectedID = rowID{providerKey: "claude", slotNum: 1, email: "alex.chen.86@example.com"}
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(Model)
}

func TestBarCellsTable(t *testing.T) {
	const width = 10
	leftValues := []float64{0, 1, 20, 21, 50, 78, 100}
	pacePositions := []struct {
		name     string
		timeLeft float64
		wantIdx  int
	}{
		{"first", 0.0, 0},
		{"middle", 0.5, 5},
		{"last", 1.0, 9},
	}

	wantFilled := map[float64]int{
		0:   0,
		1:   1, // >0% shows at least 1 filled cell
		20:  2,
		21:  2,
		50:  5,
		78:  8,
		100: 10,
	}

	for _, left := range leftValues {
		for _, pace := range pacePositions {
			cells := barCells(left, pace.timeLeft, width)
			if len(cells) != width {
				t.Fatalf("left=%.0f pace=%s: len=%d, want %d", left, pace.name, len(cells), width)
			}
			if cells[pace.wantIdx] != cellPace {
				t.Fatalf("left=%.0f pace=%s: cell[%d]=%v, want cellPace (cells=%v)", left, pace.name, pace.wantIdx, cells[pace.wantIdx], cells)
			}
			// Every cell other than pace matches expected fill/track split.
			wf := wantFilled[left]
			for i := 0; i < width; i++ {
				if i == pace.wantIdx {
					continue
				}
				wantKind := cellTrack
				if i < wf {
					wantKind = cellFill
				}
				if cells[i] != wantKind {
					t.Errorf("left=%.0f pace=%s cell[%d]=%v, want %v (cells=%v)", left, pace.name, i, cells[i], wantKind, cells)
				}
			}
		}
	}

	// Window-not-started case (100% left, no resetsAt -> full bar, no pace line, blank reset).
	now := fixedNow()
	views := buildWindowViews(&provider.UsageInfo{
		Windows: []provider.Window{{Label: "5h", Pct: 0, ResetsAt: nil}},
	}, now)
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	v := views[0]
	if v.hasReset {
		t.Fatal("expected blank reset on window not started")
	}
	if v.timeLeft >= 0 {
		t.Fatalf("expected negative timeLeft for window not started, got %v", v.timeLeft)
	}
	cells := barCells(v.leftPct, v.timeLeft, width)
	for i := 0; i < width; i++ {
		if cells[i] != cellFill {
			t.Fatalf("expected cell[%d]=cellFill, got %v", i, cells)
		}
	}

	// Ahead/behind pace separation: when quota left and time left diverge by >= 4%,
	// surplus fill past the pace line or a track gap before it must remain visible.
	for _, bw := range []int{10, 14} {
		ahead := barCells(31, 64.0/300.0, bw)
		paceIdx := -1
		for i, c := range ahead {
			if c == cellPace {
				paceIdx = i
			}
		}
		if paceIdx < 0 || paceIdx+1 >= bw || ahead[paceIdx+1] != cellFill {
			t.Fatalf("width=%d ahead (31%% vs 21.3%%): expected fill past pace, got %v", bw, ahead)
		}

		behind7d := barCells(69, 129.0/168.0, bw)
		paceIdx = -1
		for i, c := range behind7d {
			if c == cellPace {
				paceIdx = i
			}
		}
		if paceIdx <= 0 || behind7d[paceIdx-1] != cellTrack {
			t.Fatalf("width=%d behind (69%% vs 76.8%%): expected track gap before pace, got %v", bw, behind7d)
		}

		behind5h := barCells(16, 76.0/300.0, bw)
		paceIdx = -1
		for i, c := range behind5h {
			if c == cellPace {
				paceIdx = i
			}
		}
		if paceIdx <= 0 || behind5h[paceIdx-1] != cellTrack {
			t.Fatalf("width=%d behind (16%% vs 25.3%%): expected track gap before pace, got %v", bw, behind5h)
		}
	}

	// No reading yet (timeLeft < 0): empty track, no pace line.
	empty := barCells(0, -1, width)
	for i, c := range empty {
		if c != cellTrack {
			t.Fatalf("empty bar cell[%d]=%v, want cellTrack", i, c)
		}
	}
}

func TestBarStateThresholds(t *testing.T) {
	cases := []struct {
		left float64
		want barLevel
	}{
		{0, barBad},
		{1, barWarn},
		{20, barWarn},
		{21, barOK},
		{50, barOK},
		{78, barOK},
		{100, barOK},
	}
	for _, tc := range cases {
		if got := barState(tc.left); got != tc.want {
			t.Errorf("barState(%.0f) = %v, want %v", tc.left, got, tc.want)
		}
	}
}

func TestWindowLabelSpacing(t *testing.T) {
	setupTestEnv(t, nil)
	th := newTheme(true)
	g := newGlyphs(true, true)
	v := windowView{
		label:    "30d",
		leftPct:  42,
		timeLeft: 17.0 / 30.0,
		hasReset: true,
		resetDur: 17 * 24 * time.Hour,
		level:    barOK,
	}
	wide := renderSingleWideWindow(v, 10, th.rowNormal, g, true)
	if !strings.HasPrefix(wide, "30d =") {
		t.Fatalf("expected space between 30d label and bar in wide mode, got %q", wide)
	}
	narrow := renderNarrowWindowLine(v, 36, "    ", " ", th.rowNormal, g, true)
	if !strings.Contains(narrow, "30d =") {
		t.Fatalf("expected space between 30d label and bar in narrow mode, got %q", narrow)
	}
}

func TestFormatResetTable(t *testing.T) {
	cases := []struct {
		dur  time.Duration
		want string
	}{
		{42 * time.Minute, "42m"},
		{59 * time.Minute, "59m"},
		{time.Hour + 20*time.Minute, "1h20m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{5*24*time.Hour + 10*time.Hour, "5d10h"},
	}
	for _, tc := range cases {
		if got := formatReset(tc.dur); got != tc.want {
			t.Errorf("formatReset(%v) = %q, want %q", tc.dur, got, tc.want)
		}
	}
}

func TestFormatAgeAndTruncateEmail(t *testing.T) {
	if got := formatAge(12 * time.Second); got != "12s ago" {
		t.Fatalf("formatAge(12s) = %q", got)
	}
	if got := formatAge(4 * time.Minute); got != "4m ago" {
		t.Fatalf("formatAge(4m) = %q", got)
	}
	if got := formatAge(2 * time.Hour); got != "2h ago" {
		t.Fatalf("formatAge(2h) = %q", got)
	}

	// Middle-truncate local part and keep @domain intact.
	got := truncateEmail("very.long.local.address@example.com", 24, "…")
	if !strings.HasSuffix(got, "@example.com") {
		t.Fatalf("expected domain preserved, got %q", got)
	}
	if lipgloss.Width(got) > 24 {
		t.Fatalf("expected width <= 24, got %d (%q)", lipgloss.Width(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis in middle, got %q", got)
	}
}

func TestWideThreeWindowDegradation(t *testing.T) {
	// 3 windows at 99 cols should never exceed 99 cols and never wrap.
	cols := computeWideColumns(99, 3)
	if cols.barW != 4 {
		t.Fatalf("expected barW=4 for 3 windows at 99 cols, got %d", cols.barW)
	}
	if cols.aliasW != 0 || cols.planW != 0 {
		t.Fatalf("expected alias and plan dropped for 3 windows at 99 cols, got alias=%d plan=%d", cols.aliasW, cols.planW)
	}
	total := 1 + 2 + 2 + cols.emailW + cols.aliasW + cols.planW + cols.winAreaW + 1
	if total != 99 {
		t.Fatalf("expected exact total 99, got %d", total)
	}

	// At 112 cols, 3 windows with barW=4 fits WITH alias and plan.
	cols112 := computeWideColumns(112, 3)
	if cols112.barW != 4 || cols112.aliasW != 11 || cols112.planW != 7 {
		t.Fatalf("unexpected 3-window layout at 112 cols: %+v", cols112)
	}
}

// Regression test for the old stacked-cursor bug (Section 11).
func TestRegressionNoChevronAndSameHeightForSelectedRow(t *testing.T) {
	now := fixedNow()
	fetched := float64(now.Add(-20 * time.Second).Unix())
	acct := provider.AccountEntry{
		Number: 1,
		Email:  "alex.chen.86@example.com",
		Alias:  ptrStr("Personal"),
		Active: true,
		Plan:   &provider.PlanInfo{Label: "Pro"},
		Usage: &provider.UsageInfo{
			FetchedAt: ptrFloat(fetched),
			Windows: []provider.Window{
				{Label: "5h", Pct: 0, ResetsAt: nil},
				{Label: "7d", Pct: 22, ResetsAt: float64(now.Add(5*24*time.Hour + 10*time.Hour).Unix())},
			},
		},
	}
	th := newTheme(false)
	g := newGlyphs(false, true)
	cols := computeWideColumns(99, 2)
	state := rowState(acct, true, false, now)

	unselected := renderWideAccountRow(acct, state, cols, th.rowNormal, g, false, "", now)
	selected := renderWideAccountRow(acct, state, cols, th.rowSelected, g, false, "", now)

	if strings.Contains(selected, "›") || strings.Contains(selected, "▸") || strings.Contains(selected, "❯") {
		t.Fatalf("selected row must not contain arrow/chevron cursor:\n%s", selected)
	}
	hUnsel := lipgloss.Height(unselected)
	hSel := lipgloss.Height(selected)
	if hUnsel != 1 || hSel != 1 || hUnsel != hSel {
		t.Fatalf("expected height 1 for both unselected (%d) and selected (%d)", hUnsel, hSel)
	}
	if w := lipgloss.Width(selected); w != 99 {
		t.Fatalf("expected wide row width 99, got %d", w)
	}

	// Also verify narrow expanded selected row has no chevron on any line.
	narrowSel := renderNarrowAccountRow(acct, state, true, 42, th.rowSelected, g, false, "", now)
	if strings.Contains(narrowSel, "›") || strings.Contains(narrowSel, "▸") {
		t.Fatalf("narrow selected row must not contain chevron:\n%s", narrowSel)
	}
}

func TestGoldenMainScreens(t *testing.T) {
	withASCIIProfile(t)

	for _, tc := range []struct {
		name   string
		width  int
		height int
	}{
		{"main_100", 100, 18},
		{"main_70", 70, 20},
		{"main_42", 42, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := buildFixtureModel(t, tc.width, tc.height)
			assertGolden(t, tc.name, m.View())
		})
	}
}

func TestGoldenAllStates(t *testing.T) {
	withASCIIProfile(t)
	now := fixedNow()
	fresh := float64(now.Add(-30 * time.Second).Unix())
	old := float64(now.Add(-12 * time.Minute).Unix())

	makeSingleState := func(t *testing.T, acct provider.AccountEntry, loading bool) Model {
		t.Helper()
		m := buildFixtureModel(t, 100, 10)
		m.loading = loading
		m.usageOn = map[string]bool{"claude": true, "codex": false, "antigravity": false}
		m.data = map[string][]provider.AccountEntry{
			"claude":      {acct},
			"codex":       nil,
			"antigravity": nil,
		}
		m.errors = map[string]string{}
		m.selectedID = rowID{providerKey: "claude", slotNum: acct.Number, email: acct.Email}
		m.syncViewport()
		return m
	}

	t.Run("state_healthy", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Active: true, Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				FetchedAt: ptrFloat(fresh),
				Windows: []provider.Window{
					{Label: "5h", Pct: 0, ResetsAt: nil},
					{Label: "7d", Pct: 22, ResetsAt: float64(now.Add(5*24*time.Hour + 10*time.Hour).Unix())},
				},
			},
		}, false)
		assertGolden(t, "state_healthy", m.View())
	})

	t.Run("state_running_low", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 2, Email: "devwork@example.com", Alias: ptrStr("Work"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				FetchedAt: ptrFloat(fresh),
				Windows: []provider.Window{
					{Label: "5h", Pct: 82, ResetsAt: float64(now.Add(1*time.Hour + 20*time.Minute).Unix())},
					{Label: "7d", Pct: 48, ResetsAt: float64(now.Add(3*24*time.Hour + 4*time.Hour).Unix())},
				},
			},
		}, false)
		assertGolden(t, "state_running_low", m.View())
	})

	t.Run("state_exhausted", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 2, Email: "devwork@example.com", Alias: ptrStr("Work"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				FetchedAt: ptrFloat(fresh),
				Windows: []provider.Window{
					{Label: "5h", Pct: 100, ResetsAt: float64(now.Add(42 * time.Minute).Unix())},
					{Label: "7d", Pct: 39, ResetsAt: float64(now.Add(2*24*time.Hour + 9*time.Hour).Unix())},
				},
			},
		}, false)
		assertGolden(t, "state_exhausted", m.View())
	})

	t.Run("state_reading", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Plan: &provider.PlanInfo{Label: "Pro"},
		}, true)
		assertGolden(t, "state_reading", m.View())
	})

	t.Run("state_no_reading", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Plan: &provider.PlanInfo{Label: "Pro"},
		}, false)
		assertGolden(t, "state_no_reading", m.View())
	})

	t.Run("state_outdated", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				FetchedAt: ptrFloat(old),
				Windows: []provider.Window{
					{Label: "5h", Pct: 0},
					{Label: "7d", Pct: 22},
				},
			},
		}, false)
		assertGolden(t, "state_outdated", m.View())
	})

	t.Run("state_rate_limited", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				Error:             ptrStr("rate limited"),
				RetryAfterSeconds: ptrInt(240),
			},
		}, false)
		assertGolden(t, "state_rate_limited", m.View())
	})

	t.Run("state_offline", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 1, Email: "alex.chen.86@example.com", Alias: ptrStr("Personal"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				FetchedAt: ptrFloat(old),
				Error:     ptrStr("offline"),
				Windows: []provider.Window{
					{Label: "5h", Pct: 0},
					{Label: "7d", Pct: 22},
				},
			},
		}, false)
		assertGolden(t, "state_offline", m.View())
	})

	t.Run("state_login_expired", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 2, Email: "devwork@example.com", Alias: ptrStr("Work"),
			Plan: &provider.PlanInfo{Label: "Pro"},
			Usage: &provider.UsageInfo{
				Error: ptrStr("login expired"),
			},
		}, false)
		assertGolden(t, "state_login_expired", m.View())
	})

	t.Run("state_api_key", func(t *testing.T) {
		m := makeSingleState(t, provider.AccountEntry{
			Number: 3, Email: "devwork@example.com", Alias: ptrStr("Work"),
			Note: "API key (no usage)",
		}, false)
		assertGolden(t, "state_api_key", m.View())
	})

	t.Run("state_tool_empty", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 12)
		m.data = map[string][]provider.AccountEntry{}
		m.errors = map[string]string{}
		m.usageOn = map[string]bool{"claude": false, "codex": false, "antigravity": false}
		m.selectedID = rowID{providerKey: "claude", placeholder: true}
		m.syncViewport()
		assertGolden(t, "state_tool_empty", m.View())
	})

	t.Run("state_tool_error", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 12)
		m.data = map[string][]provider.AccountEntry{}
		m.errors = map[string]string{"antigravity": "permission denied"}
		m.usageOn = map[string]bool{"claude": false, "codex": false, "antigravity": true}
		m.selectedID = rowID{providerKey: "antigravity", placeholder: true}
		m.syncViewport()
		assertGolden(t, "state_tool_error", m.View())
	})

	t.Run("state_too_small", func(t *testing.T) {
		m := buildFixtureModel(t, 40, 10)
		assertGolden(t, "state_too_small", m.View())
	})

	t.Run("state_scroll_more", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 8)
		assertGolden(t, "state_scroll_more", m.View())
	})

	t.Run("state_header_success", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 14)
		m.setHeaderMsg(msgSuccess, "Switched to devwork@example.com")
		assertGolden(t, "state_header_success", m.View())
	})

	t.Run("state_header_error", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 14)
		m.setHeaderMsg(msgError, "couldn't switch - login expired")
		assertGolden(t, "state_header_error", m.View())
	})
}

func TestGoldenAllDialogs(t *testing.T) {
	withASCIIProfile(t)

	t.Run("dialog_add_pick_tool", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogAddPickTool
		m.dlg.cursor = 0
		assertGolden(t, "dialog_add_pick_tool", m.View())
	})

	t.Run("dialog_add_sign_in", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogAddSignIn
		m.dlg.provider = &provider.Claude{}
		m.dlg.stillSameLogin = true
		m.dlg.status = provider.StatusInfo{
			LoggedIn:    true,
			DisplayName: "work@example.com",
			ManagedSlot: ptrInt(2),
		}
		assertGolden(t, "dialog_add_sign_in", m.View())
	})

	t.Run("dialog_add_already_saved", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogAddAlreadySaved
		m.dlg.provider = &provider.Claude{}
		m.dlg.status = provider.StatusInfo{
			LoggedIn:    true,
			DisplayName: "devwork@example.com",
			ManagedSlot: ptrInt(2),
		}
		m.dlg.cursor = 0
		assertGolden(t, "dialog_add_already_saved", m.View())
	})

	t.Run("dialog_add_save", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogAddSave
		m.dlg.provider = &provider.Claude{}
		m.dlg.status = provider.StatusInfo{
			LoggedIn:    true,
			DisplayName: "devwork@example.com",
			Plan:        "Pro",
		}
		m.aliasInput.SetValue("Work")
		m.aliasInput.Blur()
		assertGolden(t, "dialog_add_save", m.View())
	})

	t.Run("dialog_remove_not_in_use", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m = m.openRemoveDialog(&provider.Claude{}, m.data["claude"][1])
		assertGolden(t, "dialog_remove_not_in_use", m.View())
	})

	t.Run("dialog_remove_in_use", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m = m.openRemoveDialog(&provider.Claude{}, m.data["claude"][0])
		assertGolden(t, "dialog_remove_in_use", m.View())
	})

	t.Run("dialog_remove_only", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m = m.openRemoveDialog(&provider.Codex{}, m.data["codex"][0])
		assertGolden(t, "dialog_remove_only", m.View())
	})

	t.Run("dialog_meters_on_claude", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogMetersOn
		m.dlg.provider = &provider.Claude{}
		m.dlg.cursor = 0
		assertGolden(t, "dialog_meters_on_claude", m.View())
	})

	t.Run("dialog_meters_on_antigravity", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogMetersOn
		m.dlg.provider = &provider.Antigravity{}
		m.dlg.cursor = 0
		assertGolden(t, "dialog_meters_on_antigravity", m.View())
	})

	t.Run("dialog_add_name", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.dlg.kind = dialogAddName
		m.dlg.provider = &provider.Antigravity{}
		m.aliasInput.Placeholder = "e.g. work"
		assertGolden(t, "dialog_add_name", m.View())
	})

	t.Run("dialog_install", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m = m.openInstall(&provider.Antigravity{}, nil)
		assertGolden(t, "dialog_install", m.View())
	})

	t.Run("dialog_agy_account", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m = m.openAccount(&provider.Antigravity{}, provider.AccountEntry{Number: 1, Email: "work", Active: true})
		assertGolden(t, "dialog_agy_account", m.View())
	})

	t.Run("dialog_cleanup", func(t *testing.T) {
		m := buildFixtureModel(t, 100, 18)
		m.legacy["antigravity"] = []string{
			filepath.Join(store.SlotDir(m.env.Root, "antigravity", 1), "auth.json"),
			filepath.Join(store.SlotDir(m.env.Root, "antigravity", 2), "acp_token.json"),
		}
		m = m.openCleanup()
		assertGolden(t, "dialog_cleanup", m.View())
	})

	t.Run("dialog_help", func(t *testing.T) {
		m := buildFixtureModel(t, 60, 22)
		m.dlg.kind = dialogHelp
		assertGolden(t, "dialog_help", m.View())
	})
}

func TestStartupZeroNetworkRefreshPreservesDataAndSelection(t *testing.T) {
	var netCalls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		netCalls.Add(1)
		b, _ := json.Marshal(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":         25.0,
					"limit_window_seconds": 18000,
					"reset_after_seconds":  3600,
				},
			},
		})
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(b)),
		}, nil
	})
	env, _ := setupTestEnv(t, rt)
	writeCodexSlot(t, env, "acct-1", "alpha@example.com", "plus", "Work", true)
	writeCodexSlot(t, env, "acct-2", "beta@example.com", "plus", "Personal", true)

	// Usage is ON by default. NewModel must make ZERO network calls.
	m := NewModel(env)
	if netCalls.Load() != 0 {
		t.Fatalf("NewModel made %d network calls, want 0", netCalls.Load())
	}
	if !m.usageOn["codex"] {
		t.Fatal("expected usage on by default for codex")
	}

	// Run Init() -> fetches usage asynchronously for the 2 codex slots.
	m, _ = execCmd(m, m.Init())
	if netCalls.Load() != 2 {
		t.Fatalf("expected 2 async usage calls after Init, got %d", netCalls.Load())
	}
	if !strings.Contains(m.View(), "75%") {
		t.Fatalf("expected 75%% left rendered:\n%s", m.View())
	}

	// Move selection to alpha@example.com (slot 1).
	next, _ := m.Update(keyRune('k'))
	m = next.(Model)
	if m.selectedID.email != "alpha@example.com" {
		t.Fatalf("expected alpha selected, got %+v", m.selectedID)
	}

	// Trigger refresh ('r'): existing numbers must NOT blank while loading!
	next, cmd := m.Update(keyRune('r'))
	m = next.(Model)
	if !m.loading {
		t.Fatal("expected loading=true after pressing r")
	}
	if !strings.Contains(m.View(), "75%") {
		t.Fatalf("refresh blanked existing numbers while loading:\n%s", m.View())
	}
	m, _ = execCmd(m, cmd)
	// Selection must stay on alpha@example.com after refresh completes.
	if m.selectedID.email != "alpha@example.com" {
		t.Fatalf("selection jumped after refresh: got %+v", m.selectedID)
	}
}

func TestOptimisticSwitchAlreadyActiveAndRollback(t *testing.T) {
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true
	writeCodexSlot(t, env, "acct-1", "alpha@example.com", "plus", "Work", true)
	writeCodexSlot(t, env, "acct-2", "beta@example.com", "plus", "Personal", true)

	m := NewModel(env)
	// Slot 2 (beta) is initially active and selected.
	next, _ := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if m.headerMsg.text != "Already active" {
		t.Fatalf("expected 'Already active' header msg, got %+v", m.headerMsg)
	}

	// Move up to slot 1 (alpha) and press Enter -> optimistic active dot flips immediately.
	next, _ = m.Update(keyRune('k'))
	m = next.(Model)
	next, cmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if !m.data["codex"][0].Active || m.data["codex"][1].Active {
		t.Fatalf("expected optimistic switch to slot 1 immediately: %+v", m.data["codex"])
	}
	m, _ = execCmd(m, cmd)
	if m.headerMsg.kind != msgSuccess || !strings.Contains(m.headerMsg.text, "alpha@example.com") {
		t.Fatalf("expected success header msg after switch, got %+v", m.headerMsg)
	}

	// Simulate failed switch rollback via switchDoneMsg{ok: false}.
	// Optimistically mark slot 2 active, then deliver failure -> must roll back to disk state (slot 1).
	m.data["codex"][0].Active = false
	m.data["codex"][1].Active = true
	next, _ = m.Update(switchDoneMsg{ok: false, providerKey: "codex", email: "beta@example.com", errText: "login expired"})
	m = next.(Model)
	if !m.data["codex"][0].Active || m.data["codex"][1].Active {
		t.Fatalf("expected rollback to slot 1 on switch failure: %+v", m.data["codex"])
	}
	if m.headerMsg.kind != msgError || !strings.Contains(m.headerMsg.text, "login expired") {
		t.Fatalf("expected error header msg on rollback, got %+v", m.headerMsg)
	}
}

func TestRemoveSafeDefaultsAndDialogFlows(t *testing.T) {
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true
	writeCodexSlot(t, env, "acct-1", "alpha@example.com", "plus", "Work", true)
	writeCodexSlot(t, env, "acct-2", "beta@example.com", "plus", "Personal", true)

	m := NewModel(env)
	// Slot 2 (active) is selected. Pressing 'd' opens dialog, does NOT remove immediately.
	next, _ := m.Update(keyRune('d'))
	m = next.(Model)
	if m.dlg.kind != dialogRemove {
		t.Fatalf("expected dialogRemove, got %v", m.dlg.kind)
	}
	choices := m.dialogChoices()
	if choices[m.dlg.cursor].destructive {
		t.Fatalf("default choice must never be destructive: %+v", choices[m.dlg.cursor])
	}
	if choices[m.dlg.cursor].id != "switch_remove" {
		t.Fatalf("expected switch_remove default for active account with sibling, got %+v", choices[m.dlg.cursor])
	}
	// Execute default ("Switch to slot 1, then remove").
	next, cmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if len(m.data["codex"]) != 1 || m.data["codex"][0].Email != "alpha@example.com" || !m.data["codex"][0].Active {
		t.Fatalf("expected slot 1 active and slot 2 removed, got %+v", m.data["codex"])
	}

	// Now alpha is the ONLY Codex account. Pressing 'd' must default to Cancel (not destructive!).
	next, _ = m.Update(keyRune('d'))
	m = next.(Model)
	choices = m.dialogChoices()
	if choices[m.dlg.cursor].destructive || choices[m.dlg.cursor].id != "cancel" {
		t.Fatalf("expected Cancel as safe default for only account, got cursor=%d choice=%+v", m.dlg.cursor, choices[m.dlg.cursor])
	}
	// Pressing Enter cancels safely and keeps the account.
	next, _ = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if m.dlg.kind != dialogNone || len(m.data["codex"]) != 1 {
		t.Fatalf("expected safe cancel keeping account, dlg=%v accts=%d", m.dlg.kind, len(m.data["codex"]))
	}
}

func TestMetersToggleAndAddFlow(t *testing.T) {
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true
	writeClaudeSlot(t, env, "uid-1", "one@example.com", "pro", "Personal", true)

	m := NewModel(env)
	// Meters are ON by default; pressing 'u' turns them off right away.
	next, _ := m.Update(keyRune('u'))
	m = next.(Model)
	if m.usageOn["claude"] {
		t.Fatal("expected claude meters turned off")
	}
	if !strings.Contains(m.View(), "meters off") {
		t.Fatalf("expected 'meters off' in view:\n%s", m.View())
	}

	// Pressing 'u' again opens Turn-on dialog with default "Turn on".
	next, _ = m.Update(keyRune('u'))
	m = next.(Model)
	if m.dlg.kind != dialogMetersOn {
		t.Fatalf("expected dialogMetersOn, got %v", m.dlg.kind)
	}
	if c := m.dialogChoices()[m.dlg.cursor]; c.id != "enable" {
		t.Fatalf("expected enable default for Claude, got %+v", c)
	}
	// Esc closes dialog without enabling.
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(Model)
	if m.dlg.kind != dialogNone || m.usageOn["claude"] {
		t.Fatal("expected esc to cancel meters dialog")
	}

	// Antigravity Turn-on dialog defaults to "Not now".
	m.usageOn["antigravity"] = false
	m.selectedID = rowID{providerKey: "antigravity", placeholder: true}
	next, _ = m.Update(keyRune('u'))
	m = next.(Model)
	if m.dlg.kind != dialogMetersOn {
		t.Fatalf("expected dialogMetersOn for antigravity, got %v", m.dlg.kind)
	}
	if c := m.dialogChoices()[m.dlg.cursor]; c.id != "cancel" {
		t.Fatalf("expected 'Not now' default for Antigravity, got %+v", c)
	}
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(Model)

	// Add flow: press 'a' -> Step 1 (Pick tool) -> choose Claude -> Step 2 (Sign in, since uid-1 already saved).
	next, _ = m.Update(keyRune('a'))
	m = next.(Model)
	if m.dlg.kind != dialogAddPickTool {
		t.Fatalf("expected dialogAddPickTool, got %v", m.dlg.kind)
	}
	m.dlg.cursor = 0 // Claude Code
	next, cmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if m.dlg.kind != dialogAddSignIn {
		t.Fatalf("expected dialogAddSignIn, got %v", m.dlg.kind)
	}
	// Recheck while still signed into same account -> stays on Step 2 with warning.
	next, cmd = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if m.dlg.kind != dialogAddSignIn || !m.dlg.stillSameLogin {
		t.Fatalf("expected stillSameLogin warning on recheck, got kind=%v still=%v", m.dlg.kind, m.dlg.stillSameLogin)
	}
	if box, _, _ := m.buildDialog(); !strings.Contains(box, m.dlg.status.DisplayName) || !strings.Contains(box, "Slot 1") {
		t.Fatalf("expected warning to name the signed-in saved account, got:\n%s", box)
	}

	// Sign in to a new account and recheck -> Step 4 (Save account).
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-uid-2",
			"refreshToken":     "rt-uid-2",
			"expiresAt":        float64(env.Now().Unix()+3600) * 1000,
			"subscriptionType": "max",
		},
	}, 0o600, false)
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{
			"accountUuid":  "uid-2",
			"emailAddress": "two@example.com",
		},
	}, 0o600, false)

	next, cmd = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if m.dlg.kind != dialogAddSave {
		t.Fatalf("expected dialogAddSave for new login, got %v", m.dlg.kind)
	}
	if m.dlg.status.Plan != "Max" {
		t.Fatalf("expected plan Max from Status, got %q", m.dlg.status.Plan)
	}
	m.aliasInput.SetValue("Work")
	next, cmd = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if len(m.data["claude"]) != 2 || m.data["claude"][1].Email != "two@example.com" {
		t.Fatalf("expected 2 claude accounts after save, got %+v", m.data["claude"])
	}
}

func TestMouseAndEnvironmentDetection(t *testing.T) {
	env, envMap := setupTestEnv(t, nil)
	env.ASCII = false
	env.DisableNetwork = true
	envMap["TERM"] = "linux"
	if !detectASCII(env) {
		t.Fatal("expected TERM=linux to auto-enable ASCII mode")
	}
	envMap["TERM"] = "xterm-256color"
	envMap["TORI_ASCII"] = "1"
	if !detectASCII(env) {
		t.Fatal("expected TORI_ASCII=1 to enable ASCII mode")
	}
	envMap["TORI_NO_ANIM"] = "1"
	if !detectNoAnim(env) {
		t.Fatal("expected TORI_NO_ANIM=1 to enable static spinner")
	}

	writeCodexSlot(t, env, "acct-1", "alpha@example.com", "plus", "Work", true)
	writeCodexSlot(t, env, "acct-2", "beta@example.com", "plus", "Personal", true)
	m := NewModel(env)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(Model)

	// Row 0 (alpha) is at body line 1 (line 0 is Codex tool header) -> screen Y = 2 + 1 = 3.
	next, _ = m.Update(tea.MouseMsg{X: 10, Y: 3, Action: tea.MouseActionMotion})
	m = next.(Model)
	if m.hoverID == nil || m.hoverID.email != "alpha@example.com" {
		t.Fatalf("expected hover on alpha@example.com, got %+v", m.hoverID)
	}
	// Single click selects alpha.
	next, _ = m.Update(tea.MouseMsg{X: 10, Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(Model)
	if m.selectedID.email != "alpha@example.com" {
		t.Fatalf("expected click to select alpha, got %+v", m.selectedID)
	}
	// Immediate second click double-clicks and triggers switch.
	next, cmd := m.Update(tea.MouseMsg{X: 10, Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected double-click to trigger switchCmd")
	}
	m, _ = execCmd(m, cmd)
	if !m.data["codex"][0].Active {
		t.Fatal("expected double-click switch to activate alpha")
	}
}

func TestAliasTagAndPlanBadge(t *testing.T) {
	rs := newTheme(false).rowNormal

	if got := aliasTag("Work", 10, "…"); got != "[Work]" {
		t.Fatalf("expected [Work], got %q", got)
	}
	if got := aliasTag("a-very-long-alias", 10, "…"); got != "[a-very-…]" {
		t.Fatalf("expected truncation inside brackets, got %q", got)
	}
	if got := aliasTag("", 10, "…"); got != "" {
		t.Fatalf("expected no tag for empty alias, got %q", got)
	}
	if got := planBadge("Free", 6, rs, "…"); lipgloss.Width(got) != 6 || !strings.Contains(got, " Free ") {
		t.Fatalf("expected padded 6-cell badge, got %q", got)
	}
	if got := planBadge("Enterprise", 6, rs, "…"); lipgloss.Width(got) != 6 {
		t.Fatalf("expected long plan truncated to 6 cells, got %q", got)
	}
	if got := planBadge("", 6, rs, "…"); got != "" {
		t.Fatalf("expected no badge for empty plan, got %q", got)
	}
}

func TestPlanBadgeShades(t *testing.T) {
	rs := newTheme(false).rowNormal
	seen := map[string]string{}
	for _, plan := range []string{"Free", "Plus", "Pro", "Max", "Team", "API", "Mystery"} {
		bg := fmt.Sprint(rs.planStyle(plan).GetBackground())
		if other, dup := seen[bg]; dup {
			t.Fatalf("%s and %s share a badge shade", plan, other)
		}
		seen[bg] = plan
	}
	if rs.planStyle("Max 5x").GetBackground() != rs.planStyle("max").GetBackground() {
		t.Fatal("expected a plan variant to use its tier shade")
	}
}

func TestDialogAddSignInRecheckNotFound(t *testing.T) {
	withASCIIProfile(t)
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true

	m := NewModel(env)
	claude := provider.Find("claude")
	if claude == nil {
		t.Fatal("expected claude provider")
	}

	// Initial open (not installed): no warning yet.
	m, _ = execCmd(m, checkAddStatusCmd(env, claude, false))
	if m.dlg.kind != dialogAddSignIn || m.dlg.notFound {
		t.Fatalf("expected initial dialogAddSignIn without notFound, got kind=%v notFound=%v", m.dlg.kind, m.dlg.notFound)
	}
	initialView := m.View()
	if strings.Contains(initialView, "No login found yet.") {
		t.Fatalf("did not expect warning on initial open:\n%s", initialView)
	}

	// Press Enter to re-check while still not installed -> shows warning and status message.
	next, cmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if m.dlg.kind != dialogAddSignIn || !m.dlg.notFound {
		t.Fatalf("expected dialogAddSignIn with notFound=true after recheck, got kind=%v notFound=%v", m.dlg.kind, m.dlg.notFound)
	}
	recheckView := m.View()
	if !strings.Contains(recheckView, "No login found yet.") || !strings.Contains(recheckView, "Claude Code is not") {
		t.Fatalf("expected not-found warning in dialog:\n%s", recheckView)
	}
}
