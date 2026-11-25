package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	agyGoogleTokenURL = "https://oauth2.googleapis.com/token"
	agyDailyEndpoint  = "https://daily-cloudcode-pa.googleapis.com"
	agyProdEndpoint   = "https://cloudcode-pa.googleapis.com"
	agyUserinfoURL    = "https://www.googleapis.com/oauth2/v2/userinfo"
	agyTokenSkewSec   = 60.0
)

// Antigravity implements Provider for T3 Code's Antigravity agent.
type Antigravity struct{}

func (g *Antigravity) Key() string       { return "antigravity" }
func (g *Antigravity) Title() string     { return "Antigravity" }
func (g *Antigravity) Short() string     { return "g" }
func (g *Antigravity) Names() []string   { return []string{"g", "a", "agy", "antigravity", "gemini"} }
func (g *Antigravity) LegacyDir() string { return ".aswap" }
func (g *Antigravity) SlotFile() string  { return "acp_token.json" }
func (g *Antigravity) AddHint() string   { return "tori g add <alias>" }
func (g *Antigravity) PermError() string { return "open the app to refresh" }
func (g *Antigravity) SetupHint() string {
	return "Sign in to Antigravity in T3, then run `tori g add <alias>`."
}
func (g *Antigravity) HelpTitle() string {
	return "Antigravity accounts (the agent used by T3 Code)"
}
func (g *Antigravity) SetupText() string {
	return "Setup: sign in to Antigravity in T3 as account A -> `tori g add a`;\nthen sign out and in as account B -> `tori g add b`."
}

func (g *Antigravity) Slots(env *Env) ([]store.Slot, error) {
	return store.Slots(env.Root, g.Key(), g.SlotFile())
}

func (g *Antigravity) Compact(env *Env) (bool, error) {
	return store.Compact(env.Root, g.Key(), g.SlotFile())
}

func (g *Antigravity) NoUsage(store.Meta) bool    { return false }
func (g *Antigravity) ListNote(store.Meta) string { return "" }

func (g *Antigravity) userAgent(env *Env) string {
	osType := env.GOOS
	switch osType {
	case "linux":
		osType = "linux"
	case "darwin":
		osType = "darwin"
	case "windows", "win32":
		osType = "windows"
	}
	arch := strings.ToLower(env.GOARCH)
	switch arch {
	case "amd64", "x86_64":
		arch = "x64"
	case "arm64", "aarch64":
		arch = "arm64"
	}
	return fmt.Sprintf("antigravity/hub/2.24.0 (aidev_client; os_type=%s; arch=%s; cl=963137146)", osType, arch)
}

func (g *Antigravity) livePath(env *Env) (string, error) {
	if v := env.getenv("TORI_AGY_TOKEN"); v != "" {
		return v, nil
	}
	pattern := filepath.Join(env.Home, ".t3", "userdata", "providers", "antigravity", "*", "antigravity-acp", "acp_token.json")
	found, err := filepath.Glob(pattern)
	if err == nil && len(found) > 0 {
		sort.Strings(found)
		return found[0], nil
	}
	dirPattern := filepath.Join(env.Home, ".t3", "userdata", "providers", "antigravity", "*", "antigravity-acp")
	dirs, err := filepath.Glob(dirPattern)
	if err == nil && len(dirs) > 0 {
		sort.Strings(dirs)
		return filepath.Join(dirs[0], "acp_token.json"), nil
	}
	return "", Fail("no T3 Antigravity login found. Sign in to Antigravity in T3 Code first.")
}

func (g *Antigravity) refreshToken(path string) string {
	d := store.ReadJSONMap(path)
	return getString(d, "refresh_token")
}

func (g *Antigravity) NameOf(n int, m store.Meta) string {
	if m.Email != "" {
		return m.Email
	}
	if m.Alias != "" {
		return m.Alias
	}
	return fmt.Sprintf("account-%d", n)
}

func (g *Antigravity) Label(n int, m store.Meta) string {
	if m.Email != "" {
		return DefaultLabel(g.NameOf(n, m), m)
	}
	return g.NameOf(n, m)
}

func (g *Antigravity) Current(env *Env, accts []store.Slot) *int {
	lp, err := g.livePath(env)
	if err != nil {
		return nil
	}
	live := g.refreshToken(lp)
	if live == "" {
		return nil
	}
	for _, s := range accts {
		if g.refreshToken(filepath.Join(s.Dir, g.SlotFile())) == live {
			n := s.Number
			return &n
		}
	}
	return nil
}

// ClearLive removes local acp_token.json without revoking server-side Google OAuth tokens.
// The returned restore func rewrites the backed-up bytes.
func (g *Antigravity) ClearLive(env *Env) (func() error, error) {
	lp, err := g.livePath(env)
	if err != nil {
		return nil, nil
	}
	raw, err := os.ReadFile(lp)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Remove(lp); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	restore := func() error {
		return store.WriteBytesAtomic(lp, raw, 0o600, false)
	}
	return restore, nil
}

func (g *Antigravity) Status(env *Env) (StatusInfo, error) {
	accts, _ := g.Slots(env)
	lp, err := g.livePath(env)
	if err != nil {
		return StatusInfo{LoggedIn: false, Message: "Not logged in to Antigravity."}, nil
	}
	if g.refreshToken(lp) == "" {
		return StatusInfo{LoggedIn: false, Message: "Not logged in to Antigravity."}, nil
	}
	cur := g.Current(env, accts)
	if cur == nil {
		return StatusInfo{
			LoggedIn:    true,
			DisplayName: "signed-in account",
			Message:     "Live login: present, not saved (run `tori g add <alias>`).",
		}, nil
	}
	var label string
	for _, s := range accts {
		if s.Number == *cur {
			label = g.Label(*cur, s.Meta)
			break
		}
	}
	return StatusInfo{
		LoggedIn:    true,
		DisplayName: label,
		ManagedSlot: cur,
		Message:     fmt.Sprintf("Current: slot %d (%s)", *cur, label),
	}, nil
}

func (g *Antigravity) Add(env *Env, args []string) ([]string, error) {
	lp, err := g.livePath(env)
	if err != nil {
		return nil, err
	}
	liveRT := g.refreshToken(lp)
	if liveRT == "" {
		return nil, Failf("%s has no login. Sign in to Antigravity in T3 first.", lp)
	}
	accts, err := g.Slots(env)
	if err != nil {
		return nil, err
	}
	for _, s := range accts {
		if g.refreshToken(filepath.Join(s.Dir, g.SlotFile())) == liveRT {
			if len(args) > 0 && args[0] != "" {
				s.Meta.Alias = args[0]
				if err := store.WriteJSONAtomic(filepath.Join(s.Dir, "meta.json"), s.Meta, 0o600, false); err != nil {
					return nil, err
				}
			}
			return []string{fmt.Sprintf("Already saved as slot %d: %s", s.Number, g.Label(s.Number, s.Meta))}, nil
		}
	}
	rawBytes, err := os.ReadFile(lp)
	if err != nil {
		return nil, err
	}
	n := maxSlotNumber(accts) + 1
	dir := store.SlotDir(env.Root, g.Key(), n)
	if err := store.WriteBytesAtomic(filepath.Join(dir, g.SlotFile()), rawBytes, 0o600, false); err != nil {
		return nil, err
	}
	meta := store.Meta{
		Added: env.now().Unix(),
	}
	if len(args) > 0 && args[0] != "" {
		meta.Alias = args[0]
	}
	if err := store.WriteJSONAtomic(filepath.Join(dir, "meta.json"), meta, 0o600, false); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("Added slot %d: %s", n, g.Label(n, meta))}, nil
}

func (g *Antigravity) Activate(env *Env, slot store.Slot, _ []store.Slot, cur *int) ([]string, error) {
	lp, err := g.livePath(env)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		if _, err := os.Stat(lp); err == nil {
			env.warn("the current login isn't saved; run `tori g add <alias>` first to keep it.")
		}
	}
	rawBytes, err := os.ReadFile(filepath.Join(slot.Dir, g.SlotFile()))
	if err != nil {
		return nil, Failf("slot %d is empty or corrupt; remove it and add it again.", slot.Number)
	}
	if err := store.WriteBytesAtomic(lp, rawBytes, 0o600, false); err != nil {
		return nil, err
	}
	return []string{"Restart the T3 Antigravity session to pick it up."}, nil
}

func (g *Antigravity) UsageCtx(_ *Env) any { return nil }
func (g *Antigravity) Preload(_ *Env, _ store.Slot, cache UsageCache) UsageCache {
	return cache
}

func agyFingerprint(tok map[string]any) string {
	raw := getString(tok, "client_id") + "\x00" + getString(tok, "refresh_token") + "\x00" + getString(tok, "project_id")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (g *Antigravity) accessToken(env *Env, slotDir string, tok map[string]any) (string, error) {
	fp := agyFingerprint(tok)
	now := float64(env.now().UnixNano()) / 1e9
	cached := store.ReadJSONMap(filepath.Join(slotDir, "access.json"))
	if cached != nil && getString(cached, "fingerprint") == fp {
		if at := getString(cached, "accessToken"); at != "" {
			if exp, ok := toFloat(cached["expiresAt"]); ok && exp-agyTokenSkewSec > now {
				return at, nil
			}
		}
	}
	form := url.Values{
		"client_id":     {getString(tok, "client_id")},
		"client_secret": {getString(tok, "client_secret")},
		"refresh_token": {getString(tok, "refresh_token")},
		"grant_type":    {"refresh_token"},
	}.Encode()
	tokenURL := getString(tok, "token_uri")
	if tokenURL == "" {
		tokenURL = agyGoogleTokenURL
	}
	resp, err := HTTPJSON(env, tokenURL, []byte(form), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"User-Agent":   g.userAgent(env),
	})
	if err != nil {
		return "", err
	}
	at := getString(resp, "access_token")
	if at == "" {
		return "", errors.New("no access token")
	}
	expiresIn := 3600.0
	if ei, ok := toFloat(resp["expires_in"]); ok && ei > 0 {
		expiresIn = ei
	}
	_ = store.WriteJSONAtomic(filepath.Join(slotDir, "access.json"), map[string]any{
		"accessToken": at,
		"expiresAt":   now + expiresIn,
		"fingerprint": fp,
	}, 0o600, false)
	return at, nil
}

func (g *Antigravity) dropAccessToken(slotDir string) {
	_ = os.Remove(filepath.Join(slotDir, "access.json"))
}

func (g *Antigravity) endpoints(env *Env) []string {
	lp, err := g.livePath(env)
	var settings map[string]any
	if err == nil {
		settings = store.ReadJSONMap(filepath.Join(filepath.Dir(lp), "settings.json"))
	}
	kind := getString(getMap(settings, "auth"), "type")
	if kind == "" {
		kind = "oauth-personal"
	}
	if kind == "oauth-business" {
		return []string{agyProdEndpoint, agyDailyEndpoint}
	}
	return []string{agyDailyEndpoint, agyProdEndpoint}
}

func (g *Antigravity) learnEmail(env *Env, slotDir string, meta store.Meta, at string) {
	if meta.Email != "" {
		return
	}
	resp, err := HTTPJSON(env, agyUserinfoURL, nil, map[string]string{
		"Authorization": "Bearer " + at,
	})
	if err != nil {
		return
	}
	if email := getString(resp, "email"); email != "" {
		meta.Email = email
		_ = store.WriteJSONAtomic(filepath.Join(slotDir, "meta.json"), meta, 0o600, false)
	}
}

func agyQuotaEntry(quota map[string]any) (Window, bool) {
	rf, ok := toFloat(quota["remainingFraction"])
	if !ok {
		return Window{}, false
	}
	if rf < 0 {
		rf = 0
	} else if rf > 1 {
		rf = 1
	}
	pct := math.Round((1.0-rf)*1000.0) / 10.0
	return Window{
		Pct:      pct,
		ResetsAt: quota["resetTime"],
	}, true
}

func agyAsWindows(five, seven *Window) []Window {
	var out []Window
	if five != nil {
		w := *five
		w.Label = "5h"
		out = append(out, w)
	}
	if seven != nil {
		w := *seven
		w.Label = "7d"
		out = append(out, w)
	}
	return out
}

func (g *Antigravity) parseSummary(data map[string]any) []Window {
	rawGroups, _ := data["groups"].([]any)
	hasGroupBuckets := false
	for _, rg := range rawGroups {
		if gm, ok := rg.(map[string]any); ok {
			if b, _ := gm["buckets"].([]any); len(b) > 0 {
				hasGroupBuckets = true
				break
			}
		}
	}
	if !hasGroupBuckets {
		rawGroups = []any{map[string]any{"buckets": data["buckets"]}}
	}

	type bucketItem struct {
		id    string
		entry Window
	}
	byWin := map[string][]bucketItem{"5h": nil, "weekly": nil}
	for _, rg := range rawGroups {
		gm, ok := rg.(map[string]any)
		if !ok {
			continue
		}
		buckets, _ := gm["buckets"].([]any)
		for _, rb := range buckets {
			b, ok := rb.(map[string]any)
			if !ok {
				continue
			}
			if disabled, _ := b["disabled"].(bool); disabled {
				continue
			}
			win := getString(b, "window")
			if win != "5h" && win != "weekly" {
				continue
			}
			if entry, ok := agyQuotaEntry(b); ok {
				byWin[win] = append(byWin[win], bucketItem{
					id:    getString(b, "bucketId"),
					entry: entry,
				})
			}
		}
	}

	pick := func(win, exactID string) *Window {
		items := byWin[win]
		if len(items) == 0 {
			return nil
		}
		for _, it := range items {
			if it.id == exactID {
				w := it.entry
				return &w
			}
		}
		var gemini []bucketItem
		for _, it := range items {
			if strings.HasPrefix(it.id, "gemini-") {
				gemini = append(gemini, it)
			}
		}
		pool := items
		if len(gemini) > 0 {
			pool = gemini
		}
		best := pool[0].entry
		for _, it := range pool[1:] {
			if it.entry.Pct > best.Pct {
				best = it.entry
			}
		}
		return &best
	}

	return agyAsWindows(pick("5h", "gemini-5h"), pick("weekly", "gemini-weekly"))
}

func (g *Antigravity) parseModels(data map[string]any) []Window {
	models := getMap(data, "models")
	if len(models) == 0 {
		return nil
	}
	var five, seven *Window
	for mid, rm := range models {
		m, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		if getString(m, "modelProvider") != "MODEL_PROVIDER_GOOGLE" && !strings.HasPrefix(mid, "gemini-") {
			continue
		}
		for _, attr := range []string{"quotaInfo", "weeklyQuotaInfo"} {
			q := getMap(m, attr)
			if q == nil {
				continue
			}
			entry, ok := agyQuotaEntry(q)
			if !ok {
				continue
			}
			if attr == "quotaInfo" {
				if five == nil || entry.Pct > five.Pct {
					w := entry
					five = &w
				}
			} else {
				if seven == nil || entry.Pct > seven.Pct {
					w := entry
					seven = &w
				}
			}
		}
	}
	return agyAsWindows(five, seven)
}

func (g *Antigravity) queryQuota(env *Env, at, project string) ([]Window, error) {
	reqBody := map[string]any{}
	if project != "" {
		reqBody["project"] = project
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + at,
		"User-Agent":    g.userAgent(env),
		"Content-Type":  "application/json",
	}

	ops := []struct {
		name   string
		parser func(map[string]any) []Window
	}{
		{"retrieveUserQuotaSummary", g.parseSummary},
		{"fetchAvailableModels", g.parseModels},
	}

	var lastErr error
	endpoints := g.endpoints(env)
	for _, op := range ops {
		for _, ep := range endpoints {
			resp, err := HTTPJSON(env, fmt.Sprintf("%s/v1internal:%s", ep, op.name), body, headers)
			if err != nil {
				lastErr = err
				var he *HTTPError
				if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) {
					return nil, err
				}
				continue
			}
			if parsed := op.parser(resp); len(parsed) > 0 {
				return parsed, nil
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no quota data")
}

func (g *Antigravity) Fetch(env *Env, slot store.Slot, _ bool, _ any) ([]Window, error) {
	tok := store.ReadJSONMap(filepath.Join(slot.Dir, g.SlotFile()))
	if len(tok) == 0 || getString(tok, "refresh_token") == "" || getString(tok, "client_id") == "" {
		return nil, errors.New("no token")
	}
	at, err := g.accessToken(env, slot.Dir, tok)
	if err != nil {
		return nil, err
	}
	g.learnEmail(env, slot.Dir, slot.Meta, at)
	project := getString(tok, "project_id")
	wins, err := g.queryQuota(env, at, project)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) {
			g.dropAccessToken(slot.Dir)
			at2, err2 := g.accessToken(env, slot.Dir, tok)
			if err2 != nil {
				return nil, err2
			}
			return g.queryQuota(env, at2, project)
		}
		return nil, err
	}
	return wins, nil
}
