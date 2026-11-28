package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	agyDailyEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	agyProdEndpoint  = "https://cloudcode-pa.googleapis.com"
	agyUserinfoURL   = "https://www.googleapis.com/oauth2/v2/userinfo"
	agyTokenSkewSec  = 60.0

	// agy keeps its login in this OS keyring entry, or in a token file when no
	// keyring is available. Tori only reads it, and only for opted-in meters.
	agyKeyringService = "gemini"
	agyKeyringUser    = "antigravity"

	agyInstallShURL  = "https://antigravity.google/cli/install.sh"
	agyInstallPS1URL = "https://antigravity.google/cli/install.ps1"

	// agyAccountFile marks a slot. A slot holds an account name, never a login.
	agyAccountFile = "account.json"
)

// Token copies written by earlier releases. They are never read any more;
// `tori g cleanup` deletes them.
var agyLegacyTokenFiles = []string{"auth.json", "acp_token.json", "access.json"}

// A directory holding only old token files still counts as a saved account.
var agySlotFiles = []string{agyAccountFile, "auth.json", "acp_token.json"}

// Antigravity implements SignInProvider for the Antigravity CLI (agy).
// Accounts change only through Google's own sign-in inside agy.
type Antigravity struct{}

type agyAccount struct {
	// SignedIn holds, per target ID, when Tori last signed this account in there.
	SignedIn map[string]int64 `json:"signedIn,omitempty"`
	// SignedInAt is the older single marker. It is read as the agy sign-in.
	SignedInAt int64 `json:"signedInAt,omitempty"`
}

// times returns the sign-in times by target, including an old signedInAt.
func (a agyAccount) times() map[string]int64 {
	out := make(map[string]int64, len(a.SignedIn)+1)
	for k, v := range a.SignedIn {
		out[k] = v
	}
	if a.SignedInAt > 0 {
		if _, ok := out[TargetAgy]; !ok {
			out[TargetAgy] = a.SignedInAt
		}
	}
	return out
}

func (g *Antigravity) Key() string       { return "antigravity" }
func (g *Antigravity) Title() string     { return "Antigravity" }
func (g *Antigravity) Short() string     { return "g" }
func (g *Antigravity) Names() []string   { return []string{"g", "a", "agy", "antigravity", "gemini"} }
func (g *Antigravity) LegacyDir() string { return ".aswap" }
func (g *Antigravity) SlotFile() string  { return agyAccountFile }
func (g *Antigravity) AddHint() string   { return "tori g add <name>" }
func (g *Antigravity) PermError() string { return "open agy to renew" }
func (g *Antigravity) SetupHint() string {
	return "Run `tori g login <name>` to sign in with Google."
}
func (g *Antigravity) HelpTitle() string {
	return "Antigravity accounts (agy CLI and App, and T3 Code)"
}
func (g *Antigravity) SetupText() string {
	return "Setup: `tori g install` for agy (it also covers the Antigravity App), or install Antigravity\n" +
		"in T3 Code's settings. Then `tori g login work` and `tori g login personal`.\n" +
		"Every switch is a new Google sign-in. Tori saves account names, never logins.\n" +
		"T3 Code uses its own copy of Google's runtime. Restart T3 Code or start a new chat after switching.\n" +
		"Pick targets with --agy, --t3 or --t3=<instance>; Tori remembers your last choice."
}

func (g *Antigravity) Slots(env *Env) ([]store.Slot, error) {
	return store.SlotsAny(env.Root, g.Key(), agySlotFiles...)
}

func (g *Antigravity) Compact(env *Env) (bool, error) {
	return store.CompactAny(env.Root, g.Key(), agySlotFiles...)
}

func (g *Antigravity) NoUsage(store.Meta) bool    { return false }
func (g *Antigravity) ListNote(store.Meta) string { return "" }

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

// binary locates agy on PATH or where Google's installer puts it.
func (g *Antigravity) binary(env *Env) (string, bool) {
	if v := env.getenv("TORI_AGY_BIN"); v != "" {
		return v, fileExists(v)
	}
	if env.LookPath != nil {
		if p, err := env.LookPath("agy"); err == nil {
			return p, true
		}
	}
	var p string
	switch {
	case env.GOOS == "windows":
		local := env.getenv("LOCALAPPDATA")
		if local == "" && env.Home != "" {
			local = filepath.Join(env.Home, "AppData", "Local")
		}
		if local != "" {
			p = filepath.Join(local, "agy", "bin", "agy.exe")
		}
	case env.Home != "":
		p = filepath.Join(env.Home, ".local", "bin", "agy")
	}
	if p != "" && fileExists(p) {
		return p, true
	}
	return "", false
}

// Installed reports whether any target is available: agy, or T3 Code's runtime.
func (g *Antigravity) Installed(env *Env) bool {
	return len(g.Targets(env)) > 0
}

func (g *Antigravity) agyInstalled(env *Env) bool {
	_, ok := g.binary(env)
	return ok
}

// InstallCommand returns the command line of Google's official installer.
func (g *Antigravity) InstallCommand(env *Env) string {
	if env.GOOS == "windows" {
		return "irm " + agyInstallPS1URL + " | iex"
	}
	return "curl -fsSL " + agyInstallShURL + " | bash"
}

// Install runs Google's official installer. Callers confirm with the user first.
func (g *Antigravity) Install(env *Env) ([]string, error) {
	if p, ok := g.binary(env); ok {
		return []string{fmt.Sprintf("agy is already installed at %s.", p)}, nil
	}
	var cmd *exec.Cmd
	if env.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", g.InstallCommand(env))
	} else {
		cmd = exec.Command("bash", "-c", "set -o pipefail; "+g.InstallCommand(env))
	}
	if err := env.runInteractive(cmd); err != nil {
		return nil, Failf("the agy installer failed: %v", err)
	}
	p, ok := g.binary(env)
	if !ok {
		return nil, Fail("the installer finished, but agy wasn't found. Open a new terminal and run `agy --version`.")
	}
	return []string{
		fmt.Sprintf("agy is installed at %s.", p),
		"Next: tori g login <name>",
	}, nil
}

func (g *Antigravity) run(env *Env, args ...string) error {
	bin, ok := g.binary(env)
	if !ok {
		return Fail("agy isn't installed. Run `tori g install` first.")
	}
	return env.runInteractive(exec.Command(bin, args...))
}

// signIn runs Google's own terminal sign-in. Tori never sees the credentials.
func (g *Antigravity) signIn(env *Env, who string) error {
	if !g.agyInstalled(env) {
		return Fail("agy isn't installed. Run `tori g install` first.")
	}
	if who != "" {
		env.say("Sign in to Google as %s.", who)
	} else {
		env.say("Sign in to Google with the account you want to use.")
	}
	cancel := "Ctrl+D"
	if env.GOOS == "windows" {
		cancel = "Ctrl+Z, then Enter,"
	}
	env.say("Press %s to cancel.\n", cancel)
	if err := g.run(env, "acp", "--login"); err != nil {
		var ue *UserError
		if errors.As(err, &ue) {
			return err
		}
		return Failf("Google sign-in didn't finish (%v).", err)
	}
	return nil
}

// find returns the saved account matching ref, or nil when ref names a new one.
func (g *Antigravity) find(ref string, accts []store.Slot) (*store.Slot, error) {
	if strings.HasPrefix(ref, "-") {
		return nil, Failf("unknown option '%s'; account names can't start with '-'", ref)
	}
	s, err := Resolve(g, ref, accts)
	if err == nil {
		return &s, nil
	}
	// A number is always a slot reference, never the name of a new account.
	if strings.Trim(ref, "0123456789") == "" {
		return nil, err
	}
	return nil, nil
}

func (g *Antigravity) newSlot(env *Env, name string, accts []store.Slot) (store.Slot, error) {
	n := maxSlotNumber(accts) + 1
	slot := store.Slot{
		Number: n,
		Dir:    store.SlotDir(env.Root, g.Key(), n),
		Meta:   store.Meta{Alias: name, Added: env.now().Unix()},
	}
	if err := store.WriteJSONAtomic(filepath.Join(slot.Dir, "meta.json"), slot.Meta, 0o600, false); err != nil {
		return store.Slot{}, err
	}
	return slot, nil
}

// setSignedIn marks slot number n as signed in on the given targets and unmarks
// the other slots there. n == 0 unmarks every account on those targets. Marks on
// other targets stay as they are.
func (g *Antigravity) setSignedIn(env *Env, accts []store.Slot, n int, ids []string) error {
	for _, s := range accts {
		var raw agyAccount
		store.ReadJSON(filepath.Join(s.Dir, agyAccountFile), &raw)
		times := raw.times()
		for _, id := range ids {
			if s.Number == n {
				times[id] = env.now().Unix()
			} else {
				delete(times, id)
			}
		}
		acct := agyAccount{}
		if len(times) > 0 {
			acct.SignedIn = times
		}
		if err := store.WriteJSONAtomic(filepath.Join(s.Dir, agyAccountFile), acct, 0o600, false); err != nil {
			return err
		}
	}
	return nil
}

// signInTargets signs in on each target in turn and returns the targets that
// succeeded. When one fails, the rest are skipped.
func (g *Antigravity) signInTargets(env *Env, who string) ([]Target, []string, error) {
	sel, err := g.selection(env)
	if err != nil {
		return nil, nil, err
	}
	var done []Target
	var notes []string
	for _, t := range sel {
		var err error
		if t.IsT3() {
			err = g.signInT3(env, t, who)
			if err == nil {
				if who != "" {
					notes = append(notes, fmt.Sprintf("T3 Code now uses %s. Restart T3 Code or start a new chat.", who))
				} else {
					notes = append(notes, "T3 Code is signed in. Restart T3 Code or start a new chat.")
				}
			}
		} else {
			err = g.signIn(env, who)
			if err == nil {
				notes = append(notes, "Restart any open `agy` session to use it.")
			}
		}
		if err != nil {
			if len(done) > 0 {
				names := make([]string, len(done))
				for i, d := range done {
					names[i] = d.Short
				}
				err = Failf("%s (still signed in on: %s)", err.Error(), strings.Join(names, ", "))
			}
			return done, notes, err
		}
		done = append(done, t)
	}
	return done, notes, nil
}

func targetIDs(ts []Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// Login signs in with Google. A name saves the account, or selects a saved one.
func (g *Antigravity) Login(env *Env, name string) ([]string, error) {
	accts, err := g.Slots(env)
	if err != nil {
		return nil, err
	}
	var target *store.Slot
	who := name
	if name != "" {
		if target, err = g.find(name, accts); err != nil {
			return nil, err
		}
		if target != nil {
			who = g.Label(target.Number, target.Meta)
		}
	}
	done, notes, signErr := g.signInTargets(env, who)
	if len(done) == 0 {
		return nil, signErr
	}
	ids := targetIDs(done)
	if name == "" {
		// Tori can't tell which account this is, so none stays marked there.
		if err := g.setSignedIn(env, accts, 0, ids); err != nil {
			return nil, err
		}
		if signErr != nil {
			return nil, signErr
		}
		return append([]string{"Signed in. To switch back to this account later, name it: tori g add <name>"}, notes...), nil
	}
	verb := "Signed in to"
	if target == nil {
		slot, err := g.newSlot(env, name, accts)
		if err != nil {
			return nil, err
		}
		accts = append(accts, slot)
		target = &slot
		verb = "Signed in and added"
	}
	if err := g.setSignedIn(env, accts, target.Number, ids); err != nil {
		return nil, err
	}
	ClearUsageCooldown(target.Dir)
	if signErr != nil {
		return nil, signErr
	}
	return append([]string{fmt.Sprintf("%s slot %d: %s", verb, target.Number, g.Label(target.Number, target.Meta))}, notes...), nil
}

// Add names the account agy is already signed in to, without a new sign-in.
func (g *Antigravity) Add(env *Env, args []string) ([]string, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, Fail("name the account: tori g add <name>")
	}
	if !g.agyInstalled(env) {
		return nil, Fail("agy isn't installed. Run `tori g install` first.")
	}
	accts, err := g.Slots(env)
	if err != nil {
		return nil, err
	}
	target, err := g.find(args[0], accts)
	if err != nil {
		return nil, err
	}
	verb := "Marked"
	if target == nil {
		slot, err := g.newSlot(env, args[0], accts)
		if err != nil {
			return nil, err
		}
		accts = append(accts, slot)
		target = &slot
		verb = "Added"
	}
	if err := g.setSignedIn(env, accts, target.Number, []string{TargetAgy}); err != nil {
		return nil, err
	}
	ClearUsageCooldown(target.Dir)
	return []string{
		fmt.Sprintf("%s slot %d: %s", verb, target.Number, g.Label(target.Number, target.Meta)),
		"Tori assumes agy is signed in to this account now. If it isn't, run `tori g login " + args[0] + "`.",
	}, nil
}

// Logout signs out of the chosen targets. agy has no non-interactive sign-out,
// so it opens agy to run its own /logout. T3 Code signs out through its runtime.
func (g *Antigravity) Logout(env *Env) ([]string, error) {
	sel, err := g.selection(env)
	if err != nil {
		return nil, err
	}
	var done []Target
	var failure error
	for _, t := range sel {
		if t.IsT3() {
			if err := g.signOutT3(env, t); err != nil {
				failure = err
				break
			}
		} else {
			env.say("Opening agy to sign out. If you're still signed in, type /logout.")
			env.say("Type /exit to come back.\n")
			if err := g.run(env, "-i", "/logout"); err != nil {
				failure = Failf("agy exited with an error (%v).", err)
				break
			}
		}
		done = append(done, t)
	}
	if len(done) > 0 {
		accts, err := g.Slots(env)
		if err != nil {
			return nil, err
		}
		if err := g.setSignedIn(env, accts, 0, targetIDs(done)); err != nil {
			return nil, err
		}
	}
	if failure != nil {
		return nil, failure
	}
	if len(sel) == len(g.Targets(env)) {
		return []string{"Tori no longer marks any Antigravity account as signed in."}, nil
	}
	names := make([]string, len(done))
	for i, d := range done {
		names[i] = d.Short
	}
	return []string{"Signed out of " + strings.Join(names, ", ") + ". Tori no longer marks an account there."}, nil
}

// Activate switches accounts with a new Google sign-in.
func (g *Antigravity) Activate(env *Env, slot store.Slot, accts []store.Slot, _ *int) ([]string, error) {
	done, notes, signErr := g.signInTargets(env, g.Label(slot.Number, slot.Meta))
	if len(done) > 0 {
		if err := g.setSignedIn(env, accts, slot.Number, targetIDs(done)); err != nil {
			return nil, err
		}
		ClearUsageCooldown(slot.Dir)
	}
	if signErr != nil {
		return nil, signErr
	}
	return notes, nil
}

// ClearLive is unsupported: only agy itself signs out.
func (g *Antigravity) ClearLive(*Env) (func() error, error) {
	return nil, Fail("sign out with `tori g logout`")
}

// Current returns the account last signed in through Tori on any target. It
// reads no logins, so a sign-in made directly in agy isn't detected.
func (g *Antigravity) Current(_ *Env, accts []store.Slot) *int {
	var best *int
	var bestAt int64
	for _, s := range accts {
		var acct agyAccount
		if !store.ReadJSON(filepath.Join(s.Dir, agyAccountFile), &acct) {
			continue
		}
		for _, at := range acct.times() {
			if at > bestAt {
				n := s.Number
				best, bestAt = &n, at
			}
		}
	}
	return best
}

// SignedInTargets lists the target names the slot was last signed in to.
func (g *Antigravity) SignedInTargets(env *Env, slot store.Slot) []string {
	var acct agyAccount
	store.ReadJSON(filepath.Join(slot.Dir, agyAccountFile), &acct)
	var ids []string
	for id := range acct.times() {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = g.targetShort(env, id)
	}
	return out
}

func (g *Antigravity) Status(env *Env) (StatusInfo, error) {
	if !g.Installed(env) {
		msg := "Nothing to sign in to. Run `tori g install` for agy, or sign in to Antigravity in T3 Code."
		if g.t3Pending(env) {
			msg += " Install Antigravity in T3 Code's settings first."
		}
		return StatusInfo{Message: msg}, nil
	}
	accts, err := g.Slots(env)
	if err != nil {
		return StatusInfo{}, err
	}
	cur := g.Current(env, accts)
	if cur == nil {
		return StatusInfo{Message: "No account signed in through Tori. Run `tori g login <name>`."}, nil
	}
	var label string
	var where []string
	for _, s := range accts {
		if s.Number == *cur {
			label = g.Label(s.Number, s.Meta)
			where = g.SignedInTargets(env, s)
		}
	}
	msg := fmt.Sprintf("Current: slot %d (%s), last signed in through Tori", *cur, label)
	if len(where) > 0 {
		msg += " on " + strings.Join(where, ", ")
	}
	return StatusInfo{
		LoggedIn:    true,
		DisplayName: label,
		ManagedSlot: cur,
		Message:     msg,
	}, nil
}

// LegacyTokenFiles lists token copies left behind by earlier releases.
func (g *Antigravity) LegacyTokenFiles(env *Env) []string {
	accts, _ := g.Slots(env)
	var out []string
	for _, s := range accts {
		for _, name := range agyLegacyTokenFiles {
			if p := filepath.Join(s.Dir, name); fileExists(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// DeleteLegacyTokens removes old token copies and keeps the account names.
func (g *Antigravity) DeleteLegacyTokens(env *Env) (int, error) {
	accts, err := g.Slots(env)
	if err != nil {
		return 0, err
	}
	// Write every slot marker first, so no account disappears with its tokens.
	for _, s := range accts {
		marker := filepath.Join(s.Dir, agyAccountFile)
		if !fileExists(marker) {
			if err := store.WriteJSONAtomic(marker, agyAccount{}, 0o600, false); err != nil {
				return 0, err
			}
		}
	}
	n := 0
	for _, p := range g.LegacyTokenFiles(env) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		n++
	}
	return n, nil
}

func (g *Antigravity) UsageCtx(_ *Env) any { return nil }
func (g *Antigravity) Preload(_ *Env, _ store.Slot, cache UsageCache) UsageCache {
	return cache
}

// UsageActiveOnly reports that quota exists only for the signed-in account.
func (g *Antigravity) UsageActiveOnly() bool { return true }

// agyToken is the part of agy's login that quota meters use. Tori doesn't read
// the refresh token and never renews a login itself.
type agyToken struct {
	access     string
	expiry     time.Time
	projectID  string
	authMethod string
}

func parseAgyToken(raw []byte) (agyToken, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return agyToken{}, false
	}
	tok := getMap(doc, "token")
	if tok == nil {
		tok = doc
	}
	out := agyToken{
		access:     getString(tok, "access_token"),
		projectID:  getString(doc, "project_id"),
		authMethod: getString(doc, "auth_method"),
	}
	if t, err := time.Parse(time.RFC3339Nano, getString(tok, "expiry")); err == nil {
		out.expiry = t
	}
	return out, out.access != ""
}

func (g *Antigravity) liveToken(env *Env) (agyToken, bool) {
	if env.Keyring != nil {
		if secret, err := env.Keyring.Get(agyKeyringService, agyKeyringUser); err == nil {
			if tok, ok := parseAgyToken([]byte(strings.TrimSpace(secret))); ok {
				return tok, true
			}
		}
	}
	path := env.getenv("TORI_AGY_CLI_TOKEN")
	if path == "" {
		if env.Home == "" {
			return agyToken{}, false
		}
		path = filepath.Join(env.Home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return agyToken{}, false
	}
	return parseAgyToken(raw)
}

func (g *Antigravity) userAgent(env *Env) string {
	arch := env.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("antigravity/hub/2.24.0 (aidev_client; os_type=%s; arch=%s; cl=963137146)", env.GOOS, arch)
}

func (g *Antigravity) endpoints(tok agyToken) []string {
	if strings.EqualFold(tok.authMethod, "oauth-business") || strings.EqualFold(tok.authMethod, "gcp") {
		return []string{agyProdEndpoint, agyDailyEndpoint}
	}
	return []string{agyDailyEndpoint, agyProdEndpoint}
}

func (g *Antigravity) resolveProjectID(env *Env, tok agyToken) string {
	if tok.projectID != "" {
		return tok.projectID
	}
	for _, k := range []string{"TORI_AGY_PROJECT_ID", "ANTIGRAVITY_PROJECT_ID", "GEMINI_DEFAULT_PROJECT"} {
		if v := strings.TrimSpace(env.getenv(k)); v != "" {
			return v
		}
	}
	if env.Home != "" {
		p := filepath.Join(env.Home, ".gemini", "antigravity-cli", "cache", "default_project_id.txt")
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// checkAccount stops quota from one account being shown under another, which
// happens after signing in directly in agy. It learns the slot's email once.
func (g *Antigravity) checkAccount(env *Env, slot store.Slot, at string) error {
	resp, err := HTTPJSON(env, agyUserinfoURL, nil, map[string]string{
		"Authorization": "Bearer " + at,
	})
	if err != nil {
		return err
	}
	email := strings.TrimSpace(getString(resp, "email"))
	if email == "" {
		return nil
	}
	other := &UsageError{Text: "another account is signed in"}
	if slot.Meta.Email != "" {
		if !strings.EqualFold(slot.Meta.Email, email) {
			return other
		}
		return nil
	}
	accts, err := g.Slots(env)
	if err != nil {
		return err
	}
	for _, s := range accts {
		if strings.EqualFold(s.Meta.Email, email) {
			return other
		}
	}
	slot.Meta.Email = email
	return store.WriteJSONAtomic(filepath.Join(slot.Dir, "meta.json"), slot.Meta, 0o600, false)
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

func (g *Antigravity) queryQuota(env *Env, tok agyToken) ([]Window, error) {
	reqBody := map[string]any{}
	if project := g.resolveProjectID(env, tok); project != "" {
		reqBody["project"] = project
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + tok.access,
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
	for _, op := range ops {
		for _, ep := range g.endpoints(tok) {
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

// Fetch reads quota for the signed-in account with agy's current access token.
func (g *Antigravity) Fetch(env *Env, slot store.Slot, active bool, _ any) ([]Window, error) {
	if !active {
		return nil, &UsageError{Text: "not signed in"}
	}
	tok, ok := g.liveToken(env)
	if !ok {
		return nil, &UsageError{Text: "not signed in"}
	}
	if !tok.expiry.IsZero() && float64(tok.expiry.Unix()-env.now().Unix()) <= agyTokenSkewSec {
		return nil, ErrPerm
	}
	if err := g.checkAccount(env, slot, tok.access); err != nil {
		return nil, err
	}
	return g.queryQuota(env, tok)
}
