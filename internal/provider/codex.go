package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	codexTokenURL = "https://auth.openai.com/oauth/token"
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// Codex implements Provider for Codex CLI.
type Codex struct{}

func (x *Codex) Key() string       { return "codex" }
func (x *Codex) Title() string     { return "Codex" }
func (x *Codex) Short() string     { return "x" }
func (x *Codex) Names() []string   { return []string{"x", "codex"} }
func (x *Codex) LegacyDir() string { return ".codexswap" }
func (x *Codex) SlotFile() string  { return "auth.json" }
func (x *Codex) AddHint() string   { return "tori x add" }
func (x *Codex) PermError() string { return "open codex to refresh" }
func (x *Codex) SetupHint() string { return "Sign in with `codex login`, then run `tori x add`." }
func (x *Codex) HelpTitle() string { return "Codex CLI accounts" }
func (x *Codex) SetupText() string {
	return "Setup: `codex login` as account A -> `tori x add`;\nthen `codex login` as account B -> `tori x add`.\nRestart codex after switching."
}

func (x *Codex) Slots(env *Env) ([]store.Slot, error) {
	return store.Slots(env.Root, x.Key(), x.SlotFile())
}

func (x *Codex) Compact(env *Env) (bool, error) {
	return store.Compact(env.Root, x.Key(), x.SlotFile())
}

func (x *Codex) livePath(env *Env) string {
	if v := env.getenv("TORI_CODEX_AUTH"); v != "" {
		return v
	}
	if home := env.getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json")
	}
	return filepath.Join(env.Home, ".codex", "auth.json")
}

func (x *Codex) jwtPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	return data
}

func (x *Codex) isAPIKey(auth map[string]any) bool {
	if len(auth) == 0 {
		return false
	}
	if getString(auth, "auth_mode") == "apikey" {
		return true
	}
	return len(getMap(auth, "tokens")) == 0 && getString(auth, "OPENAI_API_KEY") != ""
}

func (x *Codex) identity(auth map[string]any) (acct, email, plan string) {
	if len(auth) == 0 {
		return "", "", ""
	}
	if x.isAPIKey(auth) {
		key := getString(auth, "OPENAI_API_KEY")
		sum := sha256.Sum256([]byte(key))
		return "apikey:" + hex.EncodeToString(sum[:])[:12], "", "API key"
	}
	tokens := getMap(auth, "tokens")
	idp := x.jwtPayload(getString(tokens, "id_token"))
	info := getMap(idp, "https://api.openai.com/auth")
	plan = titleCase(getString(info, "chatgpt_plan_type"))
	acct = getString(tokens, "account_id")
	if acct == "" {
		acct = getString(info, "chatgpt_account_id")
	}
	email = getString(idp, "email")
	return acct, email, plan
}

func (x *Codex) readLive(env *Env) map[string]any {
	m := store.ReadJSONMap(x.livePath(env))
	if len(m) == 0 {
		return nil
	}
	return m
}

func (x *Codex) isKeySlot(m store.Meta) bool {
	return strings.HasPrefix(m.AccountID, "apikey:")
}

func (x *Codex) NameOf(n int, m store.Meta) string {
	if m.Email != "" {
		return m.Email
	}
	if x.isKeySlot(m) {
		return "API key"
	}
	return fmt.Sprintf("account-%d", n)
}

func (x *Codex) Label(n int, m store.Meta) string {
	return DefaultLabel(x.NameOf(n, m), m)
}

func (x *Codex) NoUsage(m store.Meta) bool {
	return x.isKeySlot(m)
}

func (x *Codex) ListNote(m store.Meta) string {
	if x.isKeySlot(m) {
		return "API key (no usage)"
	}
	return ""
}

func (x *Codex) matchSlot(accts []store.Slot, auth map[string]any) *int {
	acct, email, _ := x.identity(auth)
	if acct != "" {
		for _, s := range accts {
			if s.Meta.AccountID == acct {
				n := s.Number
				return &n
			}
		}
	}
	if email != "" {
		for _, s := range accts {
			if s.Meta.Email != "" && strings.EqualFold(s.Meta.Email, email) {
				n := s.Number
				return &n
			}
		}
	}
	return nil
}

func (x *Codex) Current(env *Env, accts []store.Slot) *int {
	return x.matchSlot(accts, x.readLive(env))
}

func (x *Codex) saveSlot(dir string, auth map[string]any, meta *store.Meta) error {
	if err := store.WriteJSONAtomic(filepath.Join(dir, "auth.json"), auth, 0o600, false); err != nil {
		return err
	}
	if meta != nil {
		if err := store.WriteJSONAtomic(filepath.Join(dir, "meta.json"), meta, 0o600, false); err != nil {
			return err
		}
	}
	return nil
}

func (x *Codex) refreshCurrentSnapshot(env *Env, accts []store.Slot) {
	live := x.readLive(env)
	n := x.matchSlot(accts, live)
	if n == nil {
		return
	}
	for _, s := range accts {
		if s.Number == *n {
			acct, email, plan := x.identity(live)
			if acct != "" {
				s.Meta.AccountID = acct
			}
			if email != "" {
				s.Meta.Email = email
			}
			if plan != "" {
				s.Meta.Plan = plan
			}
			_ = x.saveSlot(s.Dir, live, &s.Meta)
			return
		}
	}
}

// ClearLive stashes any active slot snapshot and removes local auth.json
// without revoking server-side OAuth tokens. The returned restore func undoes it.
func (x *Codex) ClearLive(env *Env) (func() error, error) {
	accts, _ := x.Slots(env)
	x.refreshCurrentSnapshot(env, accts)

	lp := x.livePath(env)
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

func (x *Codex) Status(env *Env) (StatusInfo, error) {
	live := x.readLive(env)
	if live == nil {
		return StatusInfo{LoggedIn: false, Message: "Not logged in to Codex."}, nil
	}
	accts, _ := x.Slots(env)
	cur := x.Current(env, accts)
	_, email, _ := x.identity(live)
	display := email
	if display == "" {
		if x.isAPIKey(live) {
			display = "API key"
		} else {
			display = "(unknown email)"
		}
	}
	return StatusInfo{
		LoggedIn:    true,
		DisplayName: display,
		ManagedSlot: cur,
	}, nil
}

func (x *Codex) Add(env *Env, args []string) ([]string, error) {
	live := x.readLive(env)
	if live == nil {
		return nil, Fail("not logged in. Run `codex login`, then try again.")
	}
	acct, email, plan := x.identity(live)
	if acct == "" {
		return nil, Fail("could not identify the signed-in account (no account_id).")
	}
	accts, err := x.Slots(env)
	if err != nil {
		return nil, err
	}
	if n := x.matchSlot(accts, live); n != nil {
		for _, s := range accts {
			if s.Number == *n {
				s.Meta.AccountID = acct
				if email != "" {
					s.Meta.Email = email
				}
				if plan != "" {
					s.Meta.Plan = plan
				}
				if len(args) > 0 && args[0] != "" {
					s.Meta.Alias = args[0]
				}
				if err := x.saveSlot(s.Dir, live, &s.Meta); err != nil {
					return nil, err
				}
				return []string{fmt.Sprintf("Updated slot %d: %s", s.Number, x.Label(s.Number, s.Meta))}, nil
			}
		}
	}
	n := maxSlotNumber(accts) + 1
	meta := store.Meta{
		AccountID: acct,
		Email:     email,
		Plan:      plan,
		Added:     env.now().Unix(),
	}
	if len(args) > 0 && args[0] != "" {
		meta.Alias = args[0]
	}
	dir := store.SlotDir(env.Root, x.Key(), n)
	if err := x.saveSlot(dir, live, &meta); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("Added slot %d: %s", n, x.Label(n, meta))}, nil
}

func (x *Codex) Activate(env *Env, slot store.Slot, accts []store.Slot, cur *int) ([]string, error) {
	if x.readLive(env) != nil && cur == nil {
		env.warn("the current login isn't saved; run `tori x add` first to keep it.")
	}
	x.refreshCurrentSnapshot(env, accts)
	auth := store.ReadJSONMap(filepath.Join(slot.Dir, "auth.json"))
	if len(auth) == 0 {
		return nil, Failf("slot %d is empty or corrupt; remove it and add it again.", slot.Number)
	}
	if err := store.WriteJSONAtomic(x.livePath(env), auth, 0o600, false); err != nil {
		return nil, err
	}
	return []string{"Restart codex to pick it up."}, nil
}

func (x *Codex) UsageCtx(env *Env) any {
	return x.readLive(env)
}

func (x *Codex) Preload(_ *Env, _ store.Slot, cache UsageCache) UsageCache {
	return cache
}

func (x *Codex) refreshTokens(env *Env, auth map[string]any) (map[string]any, error) {
	tokens := getMap(auth, "tokens")
	rt := getString(tokens, "refresh_token")
	if rt == "" {
		return nil, errors.New("no refresh token")
	}
	payload, err := json.Marshal(map[string]any{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": rt,
		"scope":         "openid profile email",
	})
	if err != nil {
		return nil, err
	}
	resp, err := HTTPJSON(env, codexTokenURL, payload, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "tori",
	})
	if err != nil {
		return nil, err
	}

	rawBytes, err := json.Marshal(auth)
	if err != nil {
		return nil, err
	}
	var updated map[string]any
	if err := json.Unmarshal(rawBytes, &updated); err != nil {
		return nil, err
	}
	updTokens := getMap(updated, "tokens")
	if updTokens == nil {
		updTokens = make(map[string]any)
		updated["tokens"] = updTokens
	}
	for _, k := range []string{"id_token", "access_token", "refresh_token"} {
		if v := getString(resp, k); v != "" {
			updTokens[k] = v
		}
	}
	updated["last_refresh"] = env.now().UTC().Format("2006-01-02T15:04:05Z")
	return updated, nil
}

func codexWindowLabel(sec int) string {
	switch sec {
	case 0:
		return "?"
	case 18000:
		return "5h"
	case 604800:
		return "7d"
	case 2592000:
		return "30d"
	}
	if sec%86400 == 0 && sec >= 2*86400 {
		return fmt.Sprintf("%dd", sec/86400)
	}
	if sec%3600 == 0 {
		return fmt.Sprintf("%dh", sec/3600)
	}
	m := sec / 60
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%dm", m)
}

func (x *Codex) parseUsage(env *Env, resp map[string]any) []Window {
	rl := getMap(resp, "rate_limit")
	if rl == nil {
		return nil
	}
	var wins []Window
	for _, key := range []string{"primary_window", "secondary_window"} {
		w := getMap(rl, key)
		if w == nil {
			continue
		}
		pct, ok := toFloat(w["used_percent"])
		if !ok {
			continue
		}
		var reset any = w["reset_at"]
		if reset == nil {
			if after, ok := toFloat(w["reset_after_seconds"]); ok {
				reset = env.now().Unix() + int64(after)
			}
		}
		sec := 0
		if f, ok := toFloat(w["limit_window_seconds"]); ok {
			sec = int(f)
		}
		wins = append(wins, Window{
			Label:    codexWindowLabel(sec),
			Pct:      pct,
			ResetsAt: reset,
		})
	}
	return wins
}

func (x *Codex) Fetch(env *Env, slot store.Slot, active bool, ctx any) ([]Window, error) {
	now := float64(env.now().UnixNano()) / 1e9
	var auth map[string]any
	if active {
		if m, ok := ctx.(map[string]any); ok && len(m) > 0 {
			auth = m
		}
	}
	if len(auth) == 0 {
		auth = store.ReadJSONMap(filepath.Join(slot.Dir, "auth.json"))
	}
	tokens := getMap(auth, "tokens")
	if getString(tokens, "access_token") == "" {
		return nil, errors.New("no token")
	}

	expired := func(a map[string]any) bool {
		toks := getMap(a, "tokens")
		payload := x.jwtPayload(getString(toks, "access_token"))
		if exp, ok := toFloat(payload["exp"]); ok && exp > 0 {
			return exp < now+60
		}
		return false
	}

	doRefresh := func(cur map[string]any) (map[string]any, error) {
		next, err := x.refreshTokens(env, cur)
		if err != nil {
			return nil, err
		}
		bakPath := filepath.Join(slot.Dir, "auth.json.bak")
		_ = store.WriteJSONAtomic(bakPath, cur, 0o600, false)
		if err := store.WriteJSONAtomic(filepath.Join(slot.Dir, "auth.json"), next, 0o600, false); err != nil {
			return nil, err
		}
		_ = os.Remove(bakPath)
		return next, nil
	}

	refreshed := false
	if expired(auth) {
		if active {
			return nil, ErrPerm
		}
		var err error
		auth, err = doRefresh(auth)
		if err != nil {
			return nil, err
		}
		refreshed = true
	}

	fetchNow := func(a map[string]any) (map[string]any, error) {
		t := getMap(a, "tokens")
		acctID := getString(t, "account_id")
		if acctID == "" {
			acctID = slot.Meta.AccountID
		}
		return HTTPJSON(env, codexUsageURL, nil, map[string]string{
			"Authorization":      "Bearer " + getString(t, "access_token"),
			"ChatGPT-Account-Id": acctID,
			"User-Agent":         "codex_cli_rs",
			"Accept":             "application/json",
		})
	}

	resp, err := fetchNow(auth)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == 401 && !active && !refreshed && getString(tokens, "refresh_token") != "" {
			nextAuth, rerr := doRefresh(auth)
			if rerr != nil {
				return nil, rerr
			}
			resp, err = fetchNow(nextAuth)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	return x.parseUsage(env, resp), nil
}
