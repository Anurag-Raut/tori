package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
)

// Target IDs. T3 Code instances are "t3:<instanceId>".
const (
	TargetAgy   = "agy"
	TargetT3All = "t3:*" // every T3 Code instance; only valid as a request
)

// Version is sent to the runtime in clientInfo. The CLI sets it.
var Version = "dev"

// Timeouts for the runtime's answers. Tests shorten them.
var (
	acpInitTimeout   = 90 * time.Second
	acpLogoutTimeout = 60 * time.Second
	acpAuthTimeout   = 5 * time.Minute
)

// NoTargetMsg names both ways to get an Antigravity target.
const NoTargetMsg = "No Antigravity found. Install agy (`tori g install`), or install and sign in to Antigravity in T3 Code's settings."

// Target is one place Antigravity signs in: the agy CLI (shared with the
// Antigravity App) or one T3 Code Antigravity instance.
type Target struct {
	ID    string
	Label string // full name, e.g. "Antigravity CLI & App (agy)"
	Name  string // name without the note
	Note  string // short hint shown next to the name
	Short string // name used in lists
	t3    t3Instance
}

// IsT3 reports whether the target is a T3 Code instance.
func (t Target) IsT3() bool { return strings.HasPrefix(t.ID, t3TargetPrefix) }

func t3TargetID(id string) string { return t3TargetPrefix + id }

// Targets lists the places Tori can switch Antigravity now: agy when installed,
// and each T3 Code instance whose Google runtime is installed.
func (g *Antigravity) Targets(env *Env) []Target {
	var out []Target
	if g.agyInstalled(env) {
		out = append(out, Target{
			ID: TargetAgy, Label: "Antigravity CLI & App (agy)", Name: "Antigravity CLI & App",
			Note: "(agy)", Short: "agy",
		})
	}
	var t3s []t3Instance
	for _, in := range t3Instances(env) {
		if _, err := t3ResolveRuntime(env, in); err == nil {
			t3s = append(t3s, in)
		}
	}
	for _, in := range t3s {
		name := "T3 Code"
		if len(t3s) > 1 {
			n := in.DisplayName
			if n == "" {
				n = in.ID
			}
			name = "T3 Code: " + n
		}
		out = append(out, Target{
			ID: t3TargetID(in.ID), Label: name, Name: name,
			Note: "restart or start a new chat after", Short: name, t3: in,
		})
	}
	return out
}

// ValidAuthURL reports whether raw is Google's OAuth consent page, the only link
// Tori prints or opens for a sign-in.
func ValidAuthURL(raw string) (string, bool) { return validAuthURL(raw) }

// t3Pending reports whether T3 Code has Antigravity set up but no runtime yet.
func (g *Antigravity) t3Pending(env *Env) bool {
	for _, in := range t3Instances(env) {
		if _, err := t3ResolveRuntime(env, in); err != nil {
			return true
		}
	}
	return false
}

// Remembered returns the target IDs to pre-check: the last choice that is still
// available, else every available target.
func (g *Antigravity) Remembered(env *Env) []string {
	avail := g.Targets(env)
	var out []string
	for _, id := range config.AntigravityTargets(env.Root) {
		if slices.ContainsFunc(avail, func(t Target) bool { return t.ID == id }) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		for _, t := range avail {
			out = append(out, t.ID)
		}
	}
	return out
}

// selection resolves the targets to sign in to: env.Targets, else the
// remembered choice, else everything installed. A request is remembered.
func (g *Antigravity) selection(env *Env) ([]Target, error) {
	avail := g.Targets(env)
	if len(avail) == 0 {
		return nil, Fail(NoTargetMsg)
	}
	if len(env.Targets) == 0 {
		var out []Target
		ids := g.Remembered(env)
		for _, t := range avail {
			if slices.Contains(ids, t.ID) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	var out []Target
	add := func(t Target) {
		if !slices.ContainsFunc(out, func(o Target) bool { return o.ID == t.ID }) {
			out = append(out, t)
		}
	}
	for _, id := range env.Targets {
		switch {
		case id == TargetT3All:
			found := false
			for _, t := range avail {
				if t.IsT3() {
					add(t)
					found = true
				}
			}
			if !found {
				return nil, Fail("T3 Code isn't ready for Antigravity. Install Antigravity in T3 Code's settings first.")
			}
		default:
			i := slices.IndexFunc(avail, func(t Target) bool { return t.ID == id })
			if i < 0 {
				if id == TargetAgy {
					return nil, Fail("agy isn't installed. Run `tori g install` first.")
				}
				return nil, Failf("no T3 Code Antigravity instance '%s'. Install Antigravity in T3 Code's settings first.", strings.TrimPrefix(id, t3TargetPrefix))
			}
			add(avail[i])
		}
	}
	// Keep the order stable: agy first.
	slices.SortStableFunc(out, func(a, b Target) int {
		if (a.ID == TargetAgy) != (b.ID == TargetAgy) {
			if a.ID == TargetAgy {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	ids := make([]string, len(out))
	for i, t := range out {
		ids[i] = t.ID
	}
	if err := config.SetAntigravityTargets(env.Root, ids); err != nil {
		env.warn("couldn't remember the chosen targets: " + err.Error())
	}
	return out, nil
}

// targetShort names a target ID for lists, even when it is no longer offered.
func (g *Antigravity) targetShort(env *Env, id string) string {
	for _, t := range g.Targets(env) {
		if t.ID == id {
			return t.Short
		}
	}
	if id == TargetAgy {
		return "agy"
	}
	return "T3 Code"
}

func (e *Env) context() context.Context {
	if e.Ctx != nil {
		return e.Ctx
	}
	return context.Background()
}

func (e *Env) executable() (string, error) {
	if e.Executable != nil {
		return e.Executable()
	}
	return os.Executable()
}

func (e *Env) procEnviron() []string {
	if e.ProcEnviron != nil {
		return e.ProcEnviron()
	}
	return os.Environ()
}

// acpHandshake starts the runtime and runs initialize.
func (g *Antigravity) startT3(env *Env, ctx context.Context, in t3Instance, needLogoutCap bool) (*acpClient, bool, string, error) {
	rt, err := t3ResolveRuntime(env, in)
	if err != nil {
		return nil, false, "", Fail(err.Error())
	}
	profile, acp, tmp, err := t3PrepareProfile(env, in)
	if err != nil {
		return nil, false, "", Failf("can't prepare T3 Code's profile folder (%v).", err)
	}
	exe, err := env.executable()
	if err != nil {
		return nil, false, "", Failf("can't find Tori's own path (%v).", err)
	}
	cmd := exec.Command(rt.Path, rt.Args...)
	cmd.Dir = profile
	cmd.Env = t3SpawnEnv(env, env.procEnviron(), rt, profile, tmp, t3BrowserCommand(exe, env.GOOS == "windows"))
	_ = acp
	var onURL func(string)
	if env.Stdout != nil {
		onURL = func(u string) {
			env.say("If the browser didn't open, open this link:\n%s\n", u)
		}
	}
	c, err := startACP(cmd, onURL)
	if err != nil {
		return nil, false, "", Failf("couldn't start T3 Code's Antigravity runtime (%v).", err)
	}
	params := map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "tori", "version": Version},
	}
	raw, err := c.call(ctx, "initialize", params, acpInitTimeout)
	if err != nil {
		c.stop()
		return nil, false, "", g.acpFail(err, "start T3 Code's Antigravity runtime")
	}
	var info struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
		AgentCapabilities struct {
			Auth struct {
				Logout json.RawMessage `json:"logout"`
			} `json:"auth"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(raw, &info)
	oauth := false
	for _, m := range info.AuthMethods {
		if m.ID == t3AuthOAuth {
			oauth = true
		}
	}
	if !oauth {
		c.stop()
		return nil, false, "", Fail("T3 Code's Antigravity runtime doesn't offer Google sign-in.")
	}
	hasLogout := len(info.AgentCapabilities.Auth.Logout) > 0 && string(info.AgentCapabilities.Auth.Logout) != "null" && string(info.AgentCapabilities.Auth.Logout) != "false"
	return c, hasLogout, profile, nil
}

func (g *Antigravity) acpFail(err error, doing string) error {
	switch {
	case errors.Is(err, errACPCancelled):
		return Fail("Sign-in cancelled.")
	case errors.Is(err, errACPTimeout):
		return Failf("couldn't %s: no answer in time.", doing)
	case errors.Is(err, errACPExited):
		return Failf("couldn't %s: the runtime stopped.", doing)
	}
	return Failf("couldn't %s (%v).", doing, err)
}

// signInT3 runs Google's sign-in in T3 Code's runtime: sign out first, so the
// runtime asks for Google again, then authenticate.
func (g *Antigravity) signInT3(env *Env, t Target, who string) error {
	ctx, stopSig := signal.NotifyContext(env.context(), os.Interrupt)
	defer stopSig()
	if who != "" {
		env.say("Sign in to Google as %s for %s.", who, t.Name)
	} else {
		env.say("Sign in to Google with the account you want to use in %s.", t.Name)
	}
	env.say("Starting Google's sign-in. This can take up to 30 seconds. Press Ctrl+C to cancel.")
	c, hasLogout, profile, err := g.startT3(env, ctx, t.t3, false)
	if err != nil {
		return err
	}
	defer c.stop()
	loggedOut := false
	signedOutNote := func(err error) error {
		if loggedOut && err != nil && err.Error() == "Sign-in cancelled." {
			return Fail("Sign-in cancelled. T3 Code is signed out until you sign in again.")
		}
		if loggedOut && err != nil {
			return Failf("%s T3 Code is signed out until you sign in again.", err.Error())
		}
		return err
	}
	if hasLogout {
		if _, err := c.call(ctx, "logout", map[string]any{}, acpLogoutTimeout); err != nil {
			var rpc *acpError
			if !errors.As(err, &rpc) {
				return g.acpFail(err, "sign out of T3 Code first")
			}
			// The runtime answered: nothing was signed in.
		} else {
			loggedOut = true
		}
	}
	if _, err := c.call(ctx, "authenticate", map[string]any{"methodId": t3AuthOAuth}, acpAuthTimeout); err != nil {
		return signedOutNote(g.acpFail(err, "finish Google sign-in in T3 Code"))
	}
	if !t3SignedIn(profile) {
		return signedOutNote(Fail("Google finished, but T3 Code's runtime saved no login."))
	}
	return nil
}

// signOutT3 signs T3 Code's instance out through its own runtime.
func (g *Antigravity) signOutT3(env *Env, t Target) error {
	ctx, stopSig := signal.NotifyContext(env.context(), os.Interrupt)
	defer stopSig()
	env.say("Signing %s out of Google. This can take up to 30 seconds.", t.Name)
	c, hasLogout, _, err := g.startT3(env, ctx, t.t3, true)
	if err != nil {
		return err
	}
	defer c.stop()
	if !hasLogout {
		return Failf("%s's runtime can't sign out. Sign out in T3 Code's settings.", t.Name)
	}
	if _, err := c.call(ctx, "logout", map[string]any{}, acpLogoutTimeout); err != nil {
		var rpc *acpError
		if errors.As(err, &rpc) {
			return Failf("%s couldn't sign out (%v).", t.Name, rpc)
		}
		return g.acpFail(err, "sign out of T3 Code")
	}
	return nil
}
