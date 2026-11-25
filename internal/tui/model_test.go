package tui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
	"github.com/Anurag-Raut/tori/internal/store"
)

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

func setupTUIEnv(t *testing.T, rt http.RoundTripper) (*provider.Env, map[string]string) {
	t.Helper()
	home := t.TempDir()
	envMap := map[string]string{}
	now := time.Unix(1770000000, 0).UTC()
	env := &provider.Env{
		Root:       filepath.Join(home, ".tori"),
		Home:       home,
		HTTPClient: &http.Client{Transport: rt, Timeout: 5 * time.Second},
		Now:        func() time.Time { return now },
		GOOS:       "linux",
		GOARCH:     "amd64",
		Environ:    func(k string) string { return envMap[k] },
	}
	return env, envMap
}

func writeCodexSlot(t *testing.T, env *provider.Env, id, email string, setLive bool) {
	t.Helper()
	auth := map[string]any{
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.Now().Unix() + 3600}),
			"refresh_token": "rt-" + id,
			"account_id":    id,
			"id_token": makeJWT(map[string]any{
				"email":                       email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
			}),
		},
	}
	if setLive {
		_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".codex", "auth.json"), auth, 0o600, false)
	}
	x := &provider.Codex{}
	// Save directly via Add if setLive, else write slot directly
	if setLive {
		if _, err := x.Add(env, nil); err != nil {
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
			// Skip spinner ticks so we don't infinite loop
			if _, isTick := subMsg.(spinner.TickMsg); isTick {
				continue
			}
			next, c := m.Update(subMsg)
			m = next.(Model)
			lastCmd = c
		}
		return m, lastCmd
	}
	next, nextCmd := m.Update(msg)
	return next.(Model), nextCmd
}

func TestTUIStartupEmptyLastAndNavigation(t *testing.T) {
	var netCalls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		netCalls.Add(1)
		b, _ := json.Marshal(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 25.0, "limit_window_seconds": 18000, "reset_after_seconds": 600},
			},
		})
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(b)),
		}, nil
	})
	env, _ := setupTUIEnv(t, rt)

	// Add 2 Codex accounts (Claude and Antigravity remain empty).
	writeCodexSlot(t, env, "acct-1", "alpha@example.com", true)
	writeCodexSlot(t, env, "acct-2", "beta@example.com", true)

	// Enable usage for Codex and verify NewModel itself makes ZERO network calls (instant startup).
	if err := config.SetUsage(env.Root, []string{"codex"}, true); err != nil {
		t.Fatal(err)
	}
	m := NewModel(env)
	if netCalls.Load() != 0 {
		t.Fatalf("NewModel must not make network calls on startup, got %d", netCalls.Load())
	}

	// Verify Codex (with accounts) appears before empty Claude Code and Antigravity.
	view := m.View()
	idxCodex := strings.Index(view, "Codex")
	idxClaude := strings.Index(view, "Claude Code")
	idxAgy := strings.Index(view, "Antigravity")
	if idxCodex == -1 || idxClaude == -1 || idxAgy == -1 || !(idxCodex < idxClaude && idxClaude < idxAgy) {
		t.Fatalf("expected Codex before empty Claude Code and Antigravity:\n%s", view)
	}

	// Run Init() command -> fetches usage asynchronously.
	m, _ = execCmd(m, m.Init())
	if netCalls.Load() != 2 {
		t.Fatalf("expected 2 async usage calls after Init, got %d", netCalls.Load())
	}
	if !strings.Contains(m.View(), "25%") {
		t.Fatalf("expected 25%% usage rendered after async load:\n%s", m.View())
	}

	// Active slot is 2 (index 1). Move down -> wraps to Claude (index 2).
	next, _ := m.Update(keyRune('j'))
	m = next.(Model)
	if m.cur != 2 {
		t.Fatalf("expected cur=2 after j, got %d", m.cur)
	}
	// Tab jumps to Antigravity (index 3).
	next, _ = m.Update(keyType(tea.KeyTab))
	m = next.(Model)
	if m.cur != 3 {
		t.Fatalf("expected cur=3 after Tab, got %d", m.cur)
	}
	// Up moves back to Claude (index 2).
	next, _ = m.Update(keyRune('k'))
	m = next.(Model)
	if m.cur != 2 {
		t.Fatalf("expected cur=2 after k, got %d", m.cur)
	}
}

func TestTUIAddDeleteDisclaimerAndRotateModals(t *testing.T) {
	var netCalls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		netCalls.Add(1)
		b, _ := json.Marshal(map[string]any{
			"five_hour": map[string]any{"utilization": 70.0, "resets_at": "2026-11-25T15:00:00Z"},
		})
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(b)),
		}, nil
	})
	env, _ := setupTUIEnv(t, rt)

	// Setup live Claude account (not yet added to tori) + 2 Codex slots.
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-c1",
			"refreshToken":     "rt-c1",
			"expiresAt":        float64(env.Now().Unix()+3600) * 1000,
			"subscriptionType": "pro",
		},
	}, 0o600, false)
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{"accountUuid": "uuid-c1", "emailAddress": "claude@example.com"},
	}, 0o600, false)

	writeCodexSlot(t, env, "acct-1", "one@example.com", true)
	writeCodexSlot(t, env, "acct-2", "two@example.com", true)

	m := NewModel(env)

	// Jump to empty Claude row (index 2) and press Enter -> opens Add modal.
	m.cur = 2
	next, _ := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if m.modal != modalAdd || m.modalProvider.Key() != "claude" {
		t.Fatalf("expected Add modal for claude, got modal=%v", m.modal)
	}

	// Esc closes modal cleanly.
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(Model)
	if m.modal != modalNone {
		t.Fatal("expected Esc to close modal without quitting")
	}

	// Press 'a', type alias "Work", press Enter -> adds Claude account.
	next, _ = m.Update(keyRune('a'))
	m = next.(Model)
	for _, r := range "Work" {
		next, _ = m.Update(keyRune(r))
		m = next.(Model)
	}
	next, cmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if len(m.data["claude"]) != 1 || m.data["claude"][0].Email != "claude@example.com" {
		t.Fatalf("expected claude account added, got %+v", m.data["claude"])
	}

	// Press 'u' on Claude -> opens Disclaimer modal; verify disclaimer text is rendered.
	m.cur = 0 // Claude is now first (since it has 1 account, before empty Antigravity)
	next, _ = m.Update(keyRune('u'))
	m = next.(Model)
	if m.modal != modalDisclaimer {
		t.Fatalf("expected Disclaimer modal, got %v", m.modal)
	}
	modalView := m.View()
	if !strings.Contains(modalView, "Anthropic limits subscription sign-in") {
		t.Fatalf("expected Anthropic disclaimer in modal view:\n%s", modalView)
	}

	// Cancel with 'n' -> usage stays off, 0 network calls.
	next, _ = m.Update(keyRune('n'))
	m = next.(Model)
	if m.modal != modalNone || config.UsageEnabled(env.Root, "claude") || netCalls.Load() != 0 {
		t.Fatal("expected disclaimer cancel to leave usage off with 0 network calls")
	}

	// Open disclaimer again and accept with 'y' -> turns on and triggers fetch.
	next, _ = m.Update(keyRune('u'))
	m = next.(Model)
	next, cmd = m.Update(keyRune('y'))
	m = next.(Model)
	if !config.UsageEnabled(env.Root, "claude") {
		t.Fatal("expected claude usage enabled after accepting disclaimer")
	}
	m, _ = execCmd(m, cmd)
	if netCalls.Load() != 1 || !strings.Contains(m.View(), "70%") {
		t.Fatalf("expected usage fetch after accepting disclaimer, netCalls=%d", netCalls.Load())
	}

	// Toggle off with 'u'.
	next, _ = m.Update(keyRune('u'))
	m = next.(Model)
	if config.UsageEnabled(env.Root, "claude") {
		t.Fatal("expected claude usage disabled after second u")
	}

	// Rotate ('b') -> rotates Codex (the only tool with >= 2 accounts) from slot 2 to slot 1.
	next, cmd = m.Update(keyRune('b'))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if !m.data["codex"][0].Active {
		t.Fatalf("expected codex slot 1 active after rotate, got %+v", m.data["codex"])
	}

	// Delete Codex slot 1: move to Codex slot 1, press 'd', cancel then confirm.
	for i, r := range m.rows() {
		if r.provider.Key() == "codex" && r.account != nil && r.account.Number == 1 {
			m.cur = i
			break
		}
	}
	next, _ = m.Update(keyRune('d'))
	m = next.(Model)
	if m.modal != modalDelete {
		t.Fatalf("expected Delete modal, got %v", m.modal)
	}
	next, _ = m.Update(keyRune('n'))
	m = next.(Model)
	if len(m.data["codex"]) != 2 {
		t.Fatal("expected codex slot preserved after cancel")
	}
	next, _ = m.Update(keyRune('d'))
	m = next.(Model)
	next, cmd = m.Update(keyRune('y'))
	m = next.(Model)
	m, _ = execCmd(m, cmd)
	if len(m.data["codex"]) != 1 || m.data["codex"][0].Email != "two@example.com" {
		t.Fatalf("expected slot 1 deleted and slot 2 compacted to 1, got %+v", m.data["codex"])
	}
}

func TestTUIMouseAndAdaptiveLayout(t *testing.T) {
	env, _ := setupTUIEnv(t, nil)
	writeCodexSlot(t, env, "acct-1", "one@example.com", true)
	writeCodexSlot(t, env, "acct-2", "two@example.com", true)

	m := NewModel(env)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)

	// Find exact Y coordinate of slot 1 (currently inactive, since slot 2 is active).
	layout := m.computeLayout()
	slot1Line := layout.rowToLine[0]

	// Single click selects slot 1 without switching.
	next, cmd := m.Update(tea.MouseMsg{
		X:      5,
		Y:      slot1Line,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = next.(Model)
	if m.cur != 0 || cmd != nil {
		t.Fatalf("expected single click to select row 0 without switching, cur=%d cmd=%v", m.cur, cmd)
	}

	// Second click immediately -> double-click switches to slot 1!
	next, cmd = m.Update(tea.MouseMsg{
		X:      5,
		Y:      slot1Line,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected double click to trigger switch cmd")
	}
	m, _ = execCmd(m, cmd)
	if !m.data["codex"][0].Active {
		t.Fatal("expected slot 1 active after double click")
	}

	// Wheel down moves selection.
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(Model)
	if m.cur != 1 {
		t.Fatalf("expected wheel down to move cur to 1, got %d", m.cur)
	}

	// Narrow terminal with scrolling.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 42, Height: 10})
	m = next.(Model)
	v := m.View()
	if !strings.Contains(v, "Codex") || !strings.Contains(v, "▾") {
		t.Fatalf("expected narrow scrolling view with indicator:\n%s", v)
	}

	// Tiny terminal guard.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 4})
	m = next.(Model)
	if !strings.Contains(m.View(), "terminal too small") {
		t.Fatalf("expected tiny terminal warning, got %q", m.View())
	}
}
