package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	claudeTokenURL = "https://platform.claude.com/v1/oauth/token"
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

// Claude implements Provider for Claude Code.
type Claude struct{}

func (c *Claude) Key() string       { return "claude" }
func (c *Claude) Title() string     { return "Claude Code" }
func (c *Claude) Short() string     { return "c" }
func (c *Claude) Names() []string   { return []string{"c", "claude"} }
func (c *Claude) LegacyDir() string { return ".claudeswap" }
func (c *Claude) SlotFile() string  { return "oauth.json" }
func (c *Claude) AddHint() string   { return "tori c add" }
func (c *Claude) PermError() string { return "open claude to refresh" }
func (c *Claude) SetupHint() string { return "Sign in with `claude` (/login), then run `tori c add`." }
func (c *Claude) HelpTitle() string { return "Claude Code accounts" }
func (c *Claude) SetupText() string {
	return "Setup: `claude` -> /login as account A -> `tori c add`;\nthen /login as account B -> `tori c add`."
}

func (c *Claude) Slots(env *Env) ([]store.Slot, error) {
	return store.Slots(env.Root, c.Key(), c.SlotFile())
}

func (c *Claude) Compact(env *Env) (bool, error) {
	return store.Compact(env.Root, c.Key(), c.SlotFile())
}

func (c *Claude) NameOf(n int, m store.Meta) string { return DefaultNameOf(n, m) }
func (c *Claude) Label(n int, m store.Meta) string  { return DefaultLabel(c.NameOf(n, m), m) }
func (c *Claude) NoUsage(store.Meta) bool           { return false }
func (c *Claude) ListNote(store.Meta) string        { return "" }

func (c *Claude) livePaths(env *Env) (credsPath, cfgPath string) {
	if cfgDir := env.getenv("CLAUDE_CONFIG_DIR"); cfgDir != "" {
		credsPath = filepath.Join(cfgDir, ".credentials.json")
		cfgPath = filepath.Join(cfgDir, ".claude.json")
	} else {
		credsPath = filepath.Join(env.Home, ".claude", ".credentials.json")
		cfgPath = filepath.Join(env.Home, ".claude.json")
	}
	if v := env.getenv("TORI_CLAUDE_CREDS"); v != "" {
		credsPath = v
	}
	if v := env.getenv("TORI_CLAUDE_CONFIG"); v != "" {
		cfgPath = v
	}
	return credsPath, cfgPath
}

func (c *Claude) readLive(env *Env) (oauth, account map[string]any) {
	credsPath, cfgPath := c.livePaths(env)
	creds := store.ReadJSONMap(credsPath)
	cfg := store.ReadJSONMap(cfgPath)
	if o := getMap(creds, "claudeAiOauth"); len(o) > 0 {
		oauth = o
	}
	if a := getMap(cfg, "oauthAccount"); len(a) > 0 {
		account = a
	}
	return oauth, account
}

func (c *Claude) accountID(oauth, account map[string]any) string {
	if id := getString(account, "accountUuid"); id != "" {
		return id
	}
	return getString(oauth, "accountUuid")
}

func (c *Claude) planLabel(oauth map[string]any) string {
	t := getString(oauth, "subscriptionType")
	if t == "" {
		return ""
	}
	return titleCase(t)
}

func (c *Claude) applyLive(env *Env, oauth, account map[string]any) error {
	credsPath, cfgPath := c.livePaths(env)
	creds := store.ReadJSONMap(credsPath)
	if creds == nil {
		creds = make(map[string]any)
	}
	creds["claudeAiOauth"] = oauth
	if err := store.WriteJSONAtomic(credsPath, creds, 0o600, false); err != nil {
		return err
	}

	cfg := store.ReadJSONMap(cfgPath)
	if cfg == nil {
		cfg = make(map[string]any)
	}
	if len(account) > 0 {
		cfg["oauthAccount"] = account
	}
	return store.WriteJSONAtomic(cfgPath, cfg, 0o600, true)
}

func (c *Claude) saveSlot(dir string, oauth, account map[string]any, meta *store.Meta) error {
	if err := store.WriteJSONAtomic(filepath.Join(dir, "oauth.json"), oauth, 0o600, false); err != nil {
		return err
	}
	if account == nil {
		account = map[string]any{}
	}
	if err := store.WriteJSONAtomic(filepath.Join(dir, "account.json"), account, 0o600, false); err != nil {
		return err
	}
	if meta != nil {
		if err := store.WriteJSONAtomic(filepath.Join(dir, "meta.json"), meta, 0o600, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *Claude) Current(env *Env, accts []store.Slot) *int {
	oauth, account := c.readLive(env)
	live := c.accountID(oauth, account)
	if live == "" {
		return nil
	}
	for _, s := range accts {
		if s.Meta.AccountUUID == live {
			n := s.Number
			return &n
		}
	}
	return nil
}

func (c *Claude) refreshCurrentSnapshot(env *Env, accts []store.Slot) {
	oauth, account := c.readLive(env)
	live := c.accountID(oauth, account)
	if oauth == nil || live == "" {
		return
	}
	for _, s := range accts {
		if s.Meta.AccountUUID == live {
			if p := c.planLabel(oauth); p != "" {
				s.Meta.Plan = p
			}
			acct := account
			if acct == nil {
				acct = store.ReadJSONMap(filepath.Join(s.Dir, "account.json"))
			}
			_ = c.saveSlot(s.Dir, oauth, acct, &s.Meta)
			return
		}
	}
}

// ClearLive stashes any active slot snapshot and clears only local login keys
// without revoking server-side OAuth tokens. The returned restore func undoes it.
func (c *Claude) ClearLive(env *Env) (func() error, error) {
	accts, _ := c.Slots(env)
	c.refreshCurrentSnapshot(env, accts)

	oauth, account := c.readLive(env)
	if oauth == nil && account == nil {
		return nil, nil
	}
	credsPath, cfgPath := c.livePaths(env)

	if creds := store.ReadJSONMap(credsPath); creds != nil {
		delete(creds, "claudeAiOauth")
		if err := store.WriteJSONAtomic(credsPath, creds, 0o600, false); err != nil {
			return nil, err
		}
	}
	if cfg := store.ReadJSONMap(cfgPath); cfg != nil {
		delete(cfg, "oauthAccount")
		if err := store.WriteJSONAtomic(cfgPath, cfg, 0o600, true); err != nil {
			return nil, err
		}
	}

	restore := func() error {
		if oauth != nil {
			return c.applyLive(env, oauth, account)
		}
		if len(account) > 0 {
			cfg := store.ReadJSONMap(cfgPath)
			if cfg == nil {
				cfg = make(map[string]any)
			}
			cfg["oauthAccount"] = account
			return store.WriteJSONAtomic(cfgPath, cfg, 0o600, true)
		}
		return nil
	}
	return restore, nil
}

func (c *Claude) Status(env *Env) (StatusInfo, error) {
	oauth, account := c.readLive(env)
	if oauth == nil {
		return StatusInfo{LoggedIn: false, Message: "Not logged in to Claude Code."}, nil
	}
	accts, _ := c.Slots(env)
	cur := c.Current(env, accts)
	email := getString(account, "emailAddress")
	if email == "" {
		email = "(unknown email)"
	}
	return StatusInfo{
		LoggedIn:    true,
		DisplayName: email,
		ManagedSlot: cur,
	}, nil
}

func (c *Claude) Add(env *Env, args []string) ([]string, error) {
	oauth, account := c.readLive(env)
	if oauth == nil {
		if env.GOOS == "darwin" {
			return nil, Fail("no login file found. On macOS Claude Code stores its login in the Keychain, which tori doesn't support yet.")
		}
		return nil, Fail("not logged in. Run `claude`, use /login, then try again.")
	}
	uid := c.accountID(oauth, account)
	if uid == "" {
		return nil, Fail("could not identify the signed-in account (no accountUuid).")
	}
	accts, err := c.Slots(env)
	if err != nil {
		return nil, err
	}
	email := getString(account, "emailAddress")
	for _, s := range accts {
		if s.Meta.AccountUUID == uid {
			if p := c.planLabel(oauth); p != "" {
				s.Meta.Plan = p
			}
			if email != "" {
				s.Meta.Email = email
			}
			if len(args) > 0 && args[0] != "" {
				s.Meta.Alias = args[0]
			}
			if err := c.saveSlot(s.Dir, oauth, account, &s.Meta); err != nil {
				return nil, err
			}
			return []string{fmt.Sprintf("Updated slot %d: %s", s.Number, c.Label(s.Number, s.Meta))}, nil
		}
	}
	n := maxSlotNumber(accts) + 1
	meta := store.Meta{
		AccountUUID: uid,
		Email:       email,
		Plan:        c.planLabel(oauth),
		Added:       env.now().Unix(),
	}
	if len(args) > 0 && args[0] != "" {
		meta.Alias = args[0]
	}
	dir := store.SlotDir(env.Root, c.Key(), n)
	if err := c.saveSlot(dir, oauth, account, &meta); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("Added slot %d: %s", n, c.Label(n, meta))}, nil
}

func (c *Claude) Activate(env *Env, slot store.Slot, accts []store.Slot, cur *int) ([]string, error) {
	liveOauth, _ := c.readLive(env)
	if liveOauth != nil && cur == nil {
		env.warn("the current login isn't saved; run `tori c add` first to keep it.")
	}
	c.refreshCurrentSnapshot(env, accts)
	oauth := store.ReadJSONMap(filepath.Join(slot.Dir, "oauth.json"))
	if len(oauth) == 0 {
		return nil, Failf("slot %d is empty or corrupt; remove it and add it again.", slot.Number)
	}
	account := store.ReadJSONMap(filepath.Join(slot.Dir, "account.json"))
	if err := c.applyLive(env, oauth, account); err != nil {
		return nil, err
	}
	return []string{"New account is used on the next message; restart claude if it still shows the old one."}, nil
}

func (c *Claude) UsageCtx(env *Env) any {
	oauth, _ := c.readLive(env)
	return oauth
}

func (c *Claude) Preload(env *Env, slot store.Slot, cache UsageCache) UsageCache {
	if env.RootOverridden || slot.Meta.Email == "" {
		return cache
	}
	t3, ok := c.t3CachedUsage(env.Home, slot.Meta.Email)
	if ok && t3.FetchedAt > cache.FetchedAt {
		_ = store.WriteJSONAtomic(filepath.Join(slot.Dir, "usage.json"), t3, 0o600, false)
		return t3
	}
	return cache
}

func (c *Claude) t3CachedUsage(home, email string) (UsageCache, bool) {
	if home == "" || email == "" {
		return UsageCache{}, false
	}
	data := store.ReadJSONMap(filepath.Join(home, ".t3", "caches", "claudeAgent.json"))
	if data == nil {
		return UsageCache{}, false
	}
	authEmail := getString(getMap(data, "auth"), "email")
	if !strings.EqualFold(authEmail, email) {
		return UsageCache{}, false
	}
	ul := getMap(data, "usageLimits")
	if ul == nil {
		return UsageCache{}, false
	}
	checked := getString(ul, "checkedAt")
	if checked == "" {
		return UsageCache{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, checked)
		if err != nil {
			return UsageCache{}, false
		}
	}
	rawWins, _ := ul["windows"].([]any)
	if len(rawWins) == 0 {
		return UsageCache{}, false
	}
	byID := make(map[string]map[string]any, len(rawWins))
	for _, rw := range rawWins {
		if m, ok := rw.(map[string]any); ok {
			if id := getString(m, "id"); id != "" {
				byID[id] = m
			}
		}
	}
	var out []Window
	for _, pair := range []struct{ key, lab string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		if w, ok := byID[pair.key]; ok {
			if pct, ok := toFloat(w["usedPercent"]); ok {
				out = append(out, Window{
					Label:    pair.lab,
					Pct:      pct,
					ResetsAt: w["resetsAt"],
				})
			}
		}
	}
	if len(out) == 0 {
		return UsageCache{}, false
	}
	return UsageCache{
		Windows:   out,
		FetchedAt: float64(ts.UnixNano()) / 1e9,
	}, true
}

func (c *Claude) refreshTokens(env *Env, oauth map[string]any) (map[string]any, error) {
	rt := getString(oauth, "refreshToken")
	if rt == "" {
		return nil, errors.New("no refresh token")
	}
	payload, err := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": rt,
		"client_id":     claudeClientID,
	})
	if err != nil {
		return nil, err
	}
	resp, err := HTTPJSON(env, claudeTokenURL, payload, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "tori",
	})
	if err != nil {
		return nil, err
	}
	at := getString(resp, "access_token")
	if at == "" {
		return nil, errors.New("no access_token in response")
	}
	updated := make(map[string]any, len(oauth))
	for k, v := range oauth {
		updated[k] = v
	}
	updated["accessToken"] = at
	if newRT := getString(resp, "refresh_token"); newRT != "" {
		updated["refreshToken"] = newRT
	}
	if expIn, ok := toFloat(resp["expires_in"]); ok {
		updated["expiresAt"] = (float64(env.now().Unix()) + expIn) * 1000.0
	}
	return updated, nil
}

func (c *Claude) Fetch(env *Env, slot store.Slot, active bool, ctx any) ([]Window, error) {
	now := float64(env.now().UnixNano()) / 1e9
	var oauth map[string]any
	if active {
		if m, ok := ctx.(map[string]any); ok {
			oauth = m
		}
	} else {
		oauth = store.ReadJSONMap(filepath.Join(slot.Dir, "oauth.json"))
	}
	if len(oauth) == 0 || getString(oauth, "accessToken") == "" {
		return nil, errors.New("no token")
	}

	doRefresh := func(cur map[string]any) (map[string]any, error) {
		next, err := c.refreshTokens(env, cur)
		if err != nil {
			return nil, err
		}
		bakPath := filepath.Join(slot.Dir, "oauth.json.bak")
		_ = store.WriteJSONAtomic(bakPath, cur, 0o600, false)
		if err := store.WriteJSONAtomic(filepath.Join(slot.Dir, "oauth.json"), next, 0o600, false); err != nil {
			return nil, err
		}
		_ = os.Remove(bakPath)
		return next, nil
	}

	refreshed := false
	if expMs, ok := toFloat(oauth["expiresAt"]); ok && expMs/1000.0 < now+60 {
		if active {
			return nil, ErrPerm
		}
		var err error
		oauth, err = doRefresh(oauth)
		if err != nil {
			return nil, err
		}
		refreshed = true
	}

	fetchNow := func(o map[string]any) (map[string]any, error) {
		return HTTPJSON(env, claudeUsageURL, nil, map[string]string{
			"Authorization":  "Bearer " + getString(o, "accessToken"),
			"anthropic-beta": "oauth-2025-04-20",
			"User-Agent":     "tori",
			"Accept":         "application/json",
		})
	}

	resp, err := fetchNow(oauth)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == 401 && !active && !refreshed && getString(oauth, "refreshToken") != "" {
			nextOauth, rerr := doRefresh(oauth)
			if rerr != nil {
				return nil, rerr
			}
			resp, err = fetchNow(nextOauth)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	var out []Window
	for _, pair := range []struct{ key, lab string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		v := getMap(resp, pair.key)
		if v == nil {
			continue
		}
		if pct, ok := toFloat(v["utilization"]); ok {
			out = append(out, Window{
				Label:    pair.lab,
				Pct:      pct,
				ResetsAt: v["resets_at"],
			})
		}
	}
	return out, nil
}
