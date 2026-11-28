package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Anurag-Raut/tori/internal/provider"
)

// openURLCommand is a hidden subcommand. Google's runtime calls it through
// BROWSER, so Tori opens the sign-in link once and decides what is opened.
const openURLCommand = "__open-url"

// parseTargetFlags removes --agy, --t3 and --t3=<instance> and returns the
// target IDs they name.
func parseTargetFlags(args []string) (rest, ids []string) {
	for _, a := range args {
		switch {
		case a == "--agy":
			ids = append(ids, provider.TargetAgy)
		case a == "--t3":
			ids = append(ids, provider.TargetT3All)
		case strings.HasPrefix(a, "--t3="):
			if id := strings.TrimPrefix(a, "--t3="); id != "" {
				ids = append(ids, "t3:"+id)
			} else {
				ids = append(ids, provider.TargetT3All)
			}
		default:
			rest = append(rest, a)
		}
	}
	return
}

// openerFor returns the system command that opens a link.
func openerFor(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	}
	return "xdg-open", []string{url}
}

func cmdOpenURL(raw string, ioCfg IO) int {
	u, ok := provider.ValidAuthURL(raw)
	if !ok {
		fmt.Fprintln(ioCfg.Stderr, "tori: refusing to open a link that isn't Google's sign-in page.")
		return 1
	}
	name, args := openerFor(runtime.GOOS, u)
	if err := exec.Command(name, args...).Start(); err != nil {
		fmt.Fprintf(ioCfg.Stderr, "tori: couldn't open the browser (%v).\n", err)
		return 1
	}
	return 0
}

// offerInstall asks to install agy when no Antigravity target exists. Without a
// terminal it fails and names both ways to get one.
func offerInstall(env *provider.Env, p provider.TargetedProvider, rest []string, ioCfg IO) (*provider.Env, error) {
	if len(p.Targets(env)) > 0 || hasFlag(rest, "-h", "--help") {
		return env, nil
	}
	yes := hasFlag(rest, "-y", "--yes")
	if !yes && !ioCfg.IsTTY {
		return env, provider.Fail(provider.NoTargetMsg)
	}
	fmt.Fprintln(ioCfg.Stdout, "No Antigravity app found.")
	fmt.Fprintln(ioCfg.Stdout, "Using T3 Code? Install Antigravity in T3 Code's settings, then come back. Tori never installs T3 Code.")
	fmt.Fprintf(ioCfg.Stdout, "To use agy (it also covers the Antigravity App), Tori can run Google's official installer, which may update your shell PATH:\n  %s\n\n", p.InstallCommand(env))
	ok, err := confirm("Install agy now? [y/N] ", yes, ioCfg)
	if err != nil {
		return env, err
	}
	if !ok {
		return env, provider.Fail(provider.NoTargetMsg)
	}
	out, err := p.Install(env)
	if err != nil {
		return env, err
	}
	for _, line := range out {
		fmt.Fprintln(ioCfg.Stdout, line)
	}
	return env, nil
}
