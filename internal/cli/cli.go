package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
	"github.com/Anurag-Raut/tori/internal/tui"
)

const usageText = `usage: tori [command] [args]

  tori [--ascii]            interactive picker (enter switch, a add, d remove, u meters)
  tori list                 list accounts for every tool (--no-usage, --refresh, --json)
  tori usage                show usage meters on/off per tool (Antigravity starts off)
  tori usage on|off <c|x|g|all> [--yes]   turn meters on (shows a disclaimer first) or off
  tori status               show the current account for each tool
  tori switch               rotate Claude Code and Codex to their next account
  tori c|claude <sub>       Claude Code   (e.g. tori c switch 2)
  tori x|codex <sub>        Codex         (e.g. tori x switch 2)
  tori g|a|agy <sub>        Antigravity   (e.g. tori g switch work)
  tori help                 show this help
  tori --version            print version

Per tool: list, status, add [alias], switch [num|email|alias],
          remove <ref> [--yes], alias <ref> <name|--unset>, help
  tori c add Work           tori x add Work         tori g login Work

Antigravity only (Google's own sign-in each time):
  tori g install [--yes]    run Google's official agy installer
  tori g login [name]       sign in, and save the account under name
  tori g logout             open agy to sign out
  tori g cleanup [--yes]    delete Antigravity tokens saved by older versions`

// IO configures streams and TTY state for CLI execution.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	IsTTY  bool
}

// DefaultIO returns standard OS streams and detects TTY on stdin & stdout.
func DefaultIO() IO {
	isTTY := term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
	return IO{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		IsTTY:  isTTY,
	}
}

// Execute runs the CLI with default environment and OS streams.
func Execute(args []string, version string) int {
	env := provider.NewEnv()
	ioCfg := DefaultIO()
	return Run(env, args, version, ioCfg, tui.Run)
}

// Run executes the CLI using the provided Env, IO, and TUI launcher.
func Run(env *provider.Env, args []string, version string, ioCfg IO, runTUI func(*provider.Env) int) int {
	if ioCfg.Stdout == nil {
		ioCfg.Stdout = os.Stdout
	}
	if ioCfg.Stderr == nil {
		ioCfg.Stderr = os.Stderr
	}
	if ioCfg.Stdin == nil {
		ioCfg.Stdin = os.Stdin
	}
	env.Stdin, env.Stdout, env.Stderr = ioCfg.Stdin, ioCfg.Stdout, ioCfg.Stderr
	if env.Warn == nil {
		env.Warn = func(msg string) {
			fmt.Fprintf(ioCfg.Stderr, "tori: %s\n", msg)
		}
	}

	notices, warnings := provider.MigrateAll(env)
	for _, n := range notices {
		fmt.Fprintln(ioCfg.Stderr, n)
	}
	for _, w := range warnings {
		env.Warn(w)
	}

	if hasFlag(args, "--ascii") {
		cloned := *env
		cloned.ASCII = true
		env = &cloned
		args = filterFlags(args, "--ascii")
	}

	if len(args) == 0 {
		if !ioCfg.IsTTY {
			fmt.Fprintln(ioCfg.Stderr, "tori: interactive mode needs a terminal; try `tori status`.")
			return 2
		}
		if runTUI == nil {
			runTUI = tui.Run
		}
		return runTUI(env)
	}

	cmd, rest := args[0], args[1:]

	if cmd == "--version" || cmd == "-v" || cmd == "version" {
		if version == "" {
			version = "dev"
		}
		fmt.Fprintf(ioCfg.Stdout, "tori %s\n", version)
		return 0
	}

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Fprintln(ioCfg.Stdout, usageText)
		return 0
	}

	if p := provider.Find(cmd); p != nil {
		if err := runProvider(env, p, rest, ioCfg); err != nil {
			fmt.Fprintf(ioCfg.Stderr, "tori: %s\n", err.Error())
			return 1
		}
		return 0
	}

	switch cmd {
	case "usage":
		return cmdUsage(env, rest, ioCfg)
	case "list", "ls":
		return cmdTopList(env, rest, ioCfg)
	case "status":
		return forEach(env, ioCfg, func(p provider.Provider) error {
			return printProviderStatus(env, p, ioCfg.Stdout)
		})
	case "switch", "sw":
		if len(rest) > 0 {
			fmt.Fprintln(ioCfg.Stderr, "tori: slot numbers differ per tool; use 'tori c|x|g switch <n>'.")
			return 2
		}
		return forEach(env, ioCfg, func(p provider.Provider) error {
			if _, ok := p.(provider.SignInProvider); ok {
				fmt.Fprintf(ioCfg.Stdout, "Not rotated: switching needs a sign-in (tori %s switch <account>).\n", p.Short())
				return nil
			}
			out, err := provider.Switch(env, p, "")
			if err != nil {
				return err
			}
			for _, line := range out {
				fmt.Fprintln(ioCfg.Stdout, line)
			}
			return nil
		})
	default:
		fmt.Fprintf(ioCfg.Stderr, "tori: unknown command '%s'\n", cmd)
		fmt.Fprintln(ioCfg.Stderr, usageText)
		return 2
	}
}

func forEach(env *provider.Env, ioCfg IO, fn func(provider.Provider) error) int {
	rc := 0
	for i, p := range provider.OrderedForEnv(env) {
		if i > 0 {
			fmt.Fprintln(ioCfg.Stdout)
		}
		fmt.Fprintf(ioCfg.Stdout, "== %s ==\n", p.Title())
		if err := fn(p); err != nil {
			fmt.Fprintf(ioCfg.Stderr, "tori: %s\n", err.Error())
			if rc == 0 {
				rc = 1
			}
		}
	}
	return rc
}

func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

func filterFlags(args []string, flags ...string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		skip := false
		for _, f := range flags {
			if a == f {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, a)
		}
	}
	return out
}

func confirm(prompt string, yes bool, ioCfg IO) (bool, error) {
	if yes {
		return true, nil
	}
	if !ioCfg.IsTTY {
		return false, provider.Fail("confirmation needed; re-run with --yes to skip the prompt.")
	}
	fmt.Fprint(ioCfg.Stdout, prompt)
	reader := bufio.NewReader(ioCfg.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

func cmdUsage(env *provider.Env, rest []string, ioCfg IO) int {
	yes := hasFlag(rest, "--yes", "-y")
	rest = filterFlags(rest, "--yes", "-y")

	if len(rest) == 0 {
		for _, p := range provider.All() {
			state := "off"
			if config.UsageEnabled(env.Root, p.Key()) {
				state = "on"
			}
			fmt.Fprintf(ioCfg.Stdout, "%-12s usage %s\n", p.Title(), state)
		}
		fmt.Fprintln(ioCfg.Stdout, "Change with: tori usage on|off <c|x|g|all>")
		return 0
	}
	if (rest[0] != "on" && rest[0] != "off") || len(rest) != 2 {
		fmt.Fprintln(ioCfg.Stderr, "tori: usage: tori usage [on|off <c|x|g|all>] [--yes]")
		return 1
	}

	var targets []provider.Provider
	if rest[1] == "all" {
		targets = provider.All()
	} else {
		p := provider.Find(rest[1])
		if p == nil {
			fmt.Fprintf(ioCfg.Stderr, "tori: unknown tool '%s' (use c, x, g or all)\n", rest[1])
			return 1
		}
		targets = []provider.Provider{p}
	}

	keys := make([]string, len(targets))
	namesList := make([]string, len(targets))
	for i, p := range targets {
		keys[i] = p.Key()
		namesList[i] = p.Title()
	}
	names := strings.Join(namesList, ", ")

	if rest[0] == "off" {
		if err := config.SetUsage(env.Root, keys, false); err != nil {
			fmt.Fprintf(ioCfg.Stderr, "tori: %v\n", err)
			return 1
		}
		fmt.Fprintf(ioCfg.Stdout, "Usage meters off for %s.\n", names)
		return 0
	}

	var todo []provider.Provider
	for _, p := range targets {
		if !config.UsageEnabled(env.Root, p.Key()) {
			todo = append(todo, p)
		}
	}
	if len(todo) == 0 {
		fmt.Fprintf(ioCfg.Stdout, "Usage meters are already on for %s.\n", names)
		return 0
	}

	todoKeys := make([]string, len(todo))
	todoNames := make([]string, len(todo))
	for i, p := range todo {
		todoKeys[i] = p.Key()
		todoNames[i] = p.Title()
	}
	dl := config.DisclaimerLines(todoKeys)
	if len(dl) >= 2 {
		fmt.Fprintln(ioCfg.Stdout, dl[0])
		if len(dl) > 2 {
			fmt.Fprintln(ioCfg.Stdout)
			for _, mid := range dl[1 : len(dl)-1] {
				fmt.Fprintln(ioCfg.Stdout, mid)
			}
		}
		fmt.Fprintln(ioCfg.Stdout, "\n"+dl[len(dl)-1])
		fmt.Fprintln(ioCfg.Stdout)
	}

	ok, err := confirm(fmt.Sprintf("Turn on usage meters for %s? [y/N] ", strings.Join(todoNames, ", ")), yes, ioCfg)
	if err != nil {
		fmt.Fprintf(ioCfg.Stderr, "tori: %s\n", err.Error())
		return 1
	}
	if !ok {
		fmt.Fprintln(ioCfg.Stdout, "Cancelled.")
		return 0
	}
	if err := config.SetUsage(env.Root, todoKeys, true); err != nil {
		fmt.Fprintf(ioCfg.Stderr, "tori: %v\n", err)
		return 1
	}
	fmt.Fprintf(ioCfg.Stdout, "Usage meters on for %s.\n", strings.Join(todoNames, ", "))
	return 0
}

func cmdTopList(env *provider.Env, rest []string, ioCfg IO) int {
	for _, p := range provider.All() {
		if sp, ok := p.(provider.SignInProvider); ok {
			noticeLegacyTokens(env, sp)
		}
	}
	if hasFlag(rest, "--no-usage") {
		cloned := *env
		cloned.ForceUsageOff = true
		env = &cloned
	}
	if hasFlag(rest, "--json") {
		force := hasFlag(rest, "--refresh")
		providersMap := make(map[string]provider.JSONList)
		for _, p := range provider.OrderedForEnv(env) {
			entries, cur, err := provider.Entries(env, p, true, force)
			if err != nil {
				fmt.Fprintf(ioCfg.Stderr, "tori: %s\n", err.Error())
				return 1
			}
			providersMap[p.Key()] = provider.JSONList{
				SchemaVersion:       1,
				ActiveAccountNumber: cur,
				Accounts:            entries,
			}
		}
		enc := json.NewEncoder(ioCfg.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"schemaVersion": 1,
			"providers":     providersMap,
		})
		return 0
	}

	return forEach(env, ioCfg, func(p provider.Provider) error {
		return printProviderList(env, p, rest, ioCfg.Stdout)
	})
}

func runProvider(env *provider.Env, p provider.Provider, argv []string, ioCfg IO) error {
	if len(argv) == 0 {
		argv = []string{"list"}
	}
	sub, rest := argv[0], argv[1:]

	if sub != "help" && sub != "-h" && sub != "--help" {
		_, _ = p.Compact(env)
	}

	if sp, ok := p.(provider.SignInProvider); ok {
		if sub != "cleanup" {
			noticeLegacyTokens(env, sp)
		}
		if handled, err := runSignIn(env, sp, sub, rest, ioCfg); handled {
			return err
		}
	}

	switch sub {
	case "help", "-h", "--help":
		fmt.Fprintln(ioCfg.Stdout, providerHelp(p))
		return nil
	case "list", "ls":
		return printProviderList(env, p, rest, ioCfg.Stdout)
	case "status":
		return printProviderStatus(env, p, ioCfg.Stdout)
	case "add":
		out, err := p.Add(env, rest)
		if err != nil {
			return err
		}
		for _, line := range out {
			fmt.Fprintln(ioCfg.Stdout, line)
		}
		return nil
	case "switch", "sw":
		ref := ""
		if len(rest) > 0 {
			ref = rest[0]
		}
		out, err := provider.Switch(env, p, ref)
		if err != nil {
			return err
		}
		for _, line := range out {
			fmt.Fprintln(ioCfg.Stdout, line)
		}
		return nil
	case "remove", "rm":
		yes := hasFlag(rest, "-y", "--yes")
		clean := filterFlags(rest, "-y", "--yes")
		if len(clean) == 0 {
			return provider.Failf("usage: tori %s remove <num|email|alias> [--yes]", p.Short())
		}
		accts, err := p.Slots(env)
		if err != nil {
			return err
		}
		slot, err := provider.Resolve(p, clean[0], accts)
		if err != nil {
			return err
		}
		saved := "login"
		if _, ok := p.(provider.SignInProvider); ok {
			saved = "account name"
		}
		ok, err := confirm(fmt.Sprintf("Remove slot %d: %s? This only forgets the saved %s. [y/N] ",
			slot.Number, p.Label(slot.Number, slot.Meta), saved), yes, ioCfg)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(ioCfg.Stdout, "Cancelled.")
			return nil
		}
		msg, err := provider.Remove(env, p, slot)
		if err != nil {
			return err
		}
		fmt.Fprintln(ioCfg.Stdout, msg)
		return nil
	case "alias":
		accts, err := p.Slots(env)
		if err != nil {
			return err
		}
		if len(rest) == 0 {
			for _, s := range accts {
				if s.Meta.Alias != "" {
					fmt.Fprintf(ioCfg.Stdout, "%d: %s\n", s.Number, s.Meta.Alias)
				}
			}
			return nil
		}
		if len(rest) < 2 {
			return provider.Failf("usage: tori %s alias <num|email|alias> <name|--unset>", p.Short())
		}
		unset := rest[1] == "--unset"
		msg, err := provider.SetAlias(env, p, rest[0], rest[1], unset)
		if err != nil {
			return err
		}
		fmt.Fprintln(ioCfg.Stdout, msg)
		return nil
	default:
		return provider.Failf("unknown command '%s' (try: tori %s help)", sub, p.Short())
	}
}

// runSignIn handles the commands only sign-in providers have.
func runSignIn(env *provider.Env, p provider.SignInProvider, sub string, rest []string, ioCfg IO) (bool, error) {
	yes := hasFlag(rest, "-y", "--yes")
	args := filterFlags(rest, "-y", "--yes")
	var out []string
	var err error

	switch sub {
	case "help", "-h", "--help":
		out = []string{signInHelp(p)}
	case "install":
		if !p.Installed(env) {
			fmt.Fprintf(ioCfg.Stdout, "This runs Google's official installer, which may update your shell PATH:\n  %s\n\n", p.InstallCommand(env))
			ok, cerr := confirm("Run it? [y/N] ", yes, ioCfg)
			if cerr != nil {
				return true, cerr
			}
			if !ok {
				fmt.Fprintln(ioCfg.Stdout, "Cancelled.")
				return true, nil
			}
		}
		out, err = p.Install(env)
	case "login":
		if len(args) > 1 {
			return true, provider.Failf("usage: tori %s login [name]", p.Short())
		}
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		out, err = p.Login(env, name)
	case "logout":
		out, err = p.Logout(env)
	case "cleanup":
		files := p.LegacyTokenFiles(env)
		if len(files) == 0 {
			out = []string{"No saved tokens from older versions."}
			break
		}
		fmt.Fprintln(ioCfg.Stdout, "Tokens saved by an older version of Tori:")
		for _, f := range files {
			fmt.Fprintf(ioCfg.Stdout, "  %s\n", f)
		}
		ok, cerr := confirm("Delete them? Account names are kept. [y/N] ", yes, ioCfg)
		if cerr != nil {
			return true, cerr
		}
		if !ok {
			fmt.Fprintln(ioCfg.Stdout, "Cancelled.")
			return true, nil
		}
		n, derr := p.DeleteLegacyTokens(env)
		if derr != nil {
			return true, derr
		}
		out = []string{fmt.Sprintf("Deleted %d token file(s).", n)}
	default:
		return false, nil
	}
	if err != nil {
		return true, err
	}
	for _, line := range out {
		fmt.Fprintln(ioCfg.Stdout, line)
	}
	return true, nil
}

func noticeLegacyTokens(env *provider.Env, p provider.SignInProvider) {
	if len(p.LegacyTokenFiles(env)) > 0 {
		env.Warn(fmt.Sprintf("%s tokens saved by an older version are still on disk and no longer used. Delete them with `tori %s cleanup`.", p.Title(), p.Short()))
	}
}

func signInHelp(p provider.SignInProvider) string {
	s := p.Short()
	return fmt.Sprintf(`usage: tori %[1]s <command> [args]

%[2]s

  tori %[1]s install                  run Google's official agy installer (asks first)
  tori %[1]s login [name]             sign in with Google, saving the account under name
  tori %[1]s logout                   open agy to sign out
  tori %[1]s add <name>               name the account agy is already signed in to
  tori %[1]s switch <num|email|alias> sign in to a saved account
  tori %[1]s list                     list accounts (* = last signed in through Tori)
  tori %[1]s status                   show that account
  tori %[1]s remove <num|email|alias> forget an account name (asks first; --yes skips)
  tori %[1]s alias <ref> <name>       set a short name (--unset to clear)
  tori %[1]s cleanup                  delete tokens saved by older versions (asks first)

%[3]s`, s, p.HelpTitle(), p.SetupText())
}

func printProviderList(env *provider.Env, p provider.Provider, args []string, w io.Writer) error {
	if hasFlag(args, "--no-usage") {
		cloned := *env
		cloned.ForceUsageOff = true
		env = &cloned
	}
	force := hasFlag(args, "--refresh")
	entries, cur, err := provider.Entries(env, p, true, force)
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(provider.JSONList{
			SchemaVersion:       1,
			ActiveAccountNumber: cur,
			Accounts:            entries,
		})
	}
	if len(entries) == 0 {
		fmt.Fprintf(w, "No accounts yet. %s\n", p.SetupHint())
		return nil
	}

	accts, _ := p.Slots(env)
	cols := 100
	if termW, _, err := term.GetSize(os.Stdout.Fd()); err == nil && termW > 0 {
		cols = termW
	}

	lefts := make([]string, len(entries))
	lines := make([]string, len(entries))
	leftW := 0
	maxLine := 0
	for i, e := range entries {
		mark := " "
		if e.Active {
			mark = "*"
		}
		var meta = accts[i].Meta
		left := fmt.Sprintf(" %s %d: %s", mark, e.Number, p.Label(e.Number, meta))
		line := provider.UsageLine(e.Usage, e.Note)
		lefts[i] = left
		lines[i] = line
		if len([]rune(left)) > leftW {
			leftW = len([]rune(left))
		}
		if len([]rune(line)) > maxLine {
			maxLine = len([]rune(line))
		}
	}
	inline := maxLine > 0 && leftW+3+maxLine <= cols
	for i := range entries {
		if lines[i] != "" && inline {
			fmt.Fprintf(w, "%-*s   %s\n", leftW, lefts[i], lines[i])
		} else if lines[i] != "" {
			fmt.Fprintln(w, lefts[i])
			fmt.Fprintf(w, "      %s\n", lines[i])
		} else {
			fmt.Fprintln(w, lefts[i])
		}
	}
	return nil
}

func printProviderStatus(env *provider.Env, p provider.Provider, w io.Writer) error {
	st, err := p.Status(env)
	if err != nil {
		return err
	}
	if st.Message != "" {
		fmt.Fprintln(w, st.Message)
		return nil
	}
	if !st.LoggedIn {
		return nil
	}
	fmt.Fprintf(w, "Current: %s\n", st.DisplayName)
	if st.ManagedSlot != nil {
		fmt.Fprintf(w, "Managed: slot %d\n", *st.ManagedSlot)
	} else {
		fmt.Fprintf(w, "Managed: no (run `%s`)\n", p.AddHint())
	}
	return nil
}

func providerHelp(p provider.Provider) string {
	s := p.Short()
	return fmt.Sprintf(`usage: tori %s <command> [args]

%s

  tori %s list                     list accounts (* = current), with usage if on
                               (--json; with usage meters on: --no-usage hides them,
                               --refresh re-reads now; see `+"`tori usage`"+`)
  tori %s status                   show the signed-in account
  tori %s add [alias]              save the account currently signed in
  tori %s switch                   rotate to the next account
  tori %s switch <num|email|alias> switch to a specific account
  tori %s remove <num|email|alias> forget an account (asks first; --yes skips)
  tori %s alias <ref> <name>       set a short name (--unset to clear)

%s`, s, p.HelpTitle(), s, s, s, s, s, s, s, p.SetupText())
}
