package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/store"
)

// ErrPerm indicates an active account's token expired and the official app must refresh it.
var ErrPerm = errors.New("permission/expired active login")

// UserError represents a user-facing error formatted as "tori: <msg>".
type UserError struct {
	Msg string
}

func (e *UserError) Error() string { return e.Msg }

// Fail returns a *UserError.
func Fail(msg string) error { return &UserError{Msg: msg} }

// Failf returns a formatted *UserError.
func Failf(format string, args ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

// HTTPError represents a non-2xx HTTP response.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d", e.StatusCode)
}

// HTTPStatusText maps HTTP status codes to concise user-facing labels.
func HTTPStatusText(code int) string {
	switch code {
	case 400, 401, 403:
		return "login expired"
	case 429:
		return "rate limited"
	default:
		return fmt.Sprintf("http %d", code)
	}
}

// Env holds runtime configuration, filesystem overrides, clock, and HTTP client.
type Env struct {
	Root           string
	RootOverridden bool
	Home           string
	HTTPClient     *http.Client
	Now            func() time.Time
	GOOS           string
	GOARCH         string
	ForceUsageOff  bool
	DisableNetwork bool
	Warn           func(string)
	Environ        func(string) string
}

// NewEnv constructs a default Env from process environment variables.
func NewEnv() *Env {
	root, overridden := config.RootDir()
	return &Env{
		Root:           root,
		RootOverridden: overridden,
		Home:           config.HomeDir(),
		HTTPClient:     &http.Client{Timeout: 8 * time.Second},
		Now:            time.Now,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Warn: func(msg string) {
			fmt.Fprintf(os.Stderr, "tori: %s\n", msg)
		},
		Environ: os.Getenv,
	}
}

func (e *Env) getenv(k string) string {
	if e.Environ != nil {
		return e.Environ(k)
	}
	return os.Getenv(k)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) client() *http.Client {
	if e.HTTPClient != nil {
		return e.HTTPClient
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (e *Env) warn(msg string) {
	if e.Warn != nil {
		e.Warn(msg)
	}
}

// IsUsageEnabled returns true if usage meters are enabled for key and not forced off.
func (e *Env) IsUsageEnabled(key string) bool {
	if e.ForceUsageOff {
		return false
	}
	return config.UsageEnabled(e.Root, key)
}

// Window represents a single quota window (e.g. 5h, 7d, 30d).
type Window struct {
	Label    string  `json:"label"`
	Pct      float64 `json:"pct"`
	ResetsAt any     `json:"resetsAt"`
}

// UsageCache is the on-disk schema for usage.json in each slot.
type UsageCache struct {
	Windows   []Window       `json:"windows,omitempty"`
	FiveHour  map[string]any `json:"fiveHour,omitempty"`
	SevenDay  map[string]any `json:"sevenDay,omitempty"`
	FetchedAt float64        `json:"fetchedAt,omitempty"`
}

// UsageInfo is the runtime usage summary returned in entries and --json.
type UsageInfo struct {
	Windows    []Window `json:"windows"`
	FetchedAt  *float64 `json:"fetchedAt"`
	AgeSeconds *int     `json:"ageSeconds"`
	Error      *string  `json:"error"`
}

// PlanInfo matches the --json plan object {"label": "..."}.
type PlanInfo struct {
	Label string `json:"label"`
}

// AccountEntry represents one saved account row for CLI list, --json, and the TUI.
type AccountEntry struct {
	Number int        `json:"number"`
	Email  string     `json:"email"`
	Alias  *string    `json:"alias"`
	Active bool       `json:"active"`
	Plan   *PlanInfo  `json:"plan"`
	Usage  *UsageInfo `json:"usage"`
	Note   string     `json:"-"`
}

// JSONList is the schemaVersion 1 JSON payload for `tori <tool> list --json`.
type JSONList struct {
	SchemaVersion       int            `json:"schemaVersion"`
	ActiveAccountNumber *int           `json:"activeAccountNumber"`
	Accounts            []AccountEntry `json:"accounts"`
}

// StatusInfo describes the live signed-in login state for a provider.
type StatusInfo struct {
	LoggedIn    bool
	DisplayName string // email or "API key" or "(unknown email)"
	ManagedSlot *int   // nil if not saved in tori
	Message     string // custom status line (e.g. Antigravity)
}

// Provider defines the contract for an AI tool account switcher.
type Provider interface {
	Key() string
	Title() string
	Short() string
	Names() []string
	LegacyDir() string
	SlotFile() string
	AddHint() string
	PermError() string
	SetupHint() string
	HelpTitle() string
	SetupText() string

	Slots(env *Env) ([]store.Slot, error)
	Compact(env *Env) (bool, error)
	Current(env *Env, accts []store.Slot) *int
	Status(env *Env) (StatusInfo, error)
	Add(env *Env, args []string) ([]string, error)
	Activate(env *Env, slot store.Slot, accts []store.Slot, cur *int) ([]string, error)

	NameOf(n int, m store.Meta) string
	Label(n int, m store.Meta) string
	NoUsage(m store.Meta) bool
	ListNote(m store.Meta) string

	UsageCtx(env *Env) any
	Preload(env *Env, slot store.Slot, cache UsageCache) UsageCache
	Fetch(env *Env, slot store.Slot, active bool, ctx any) ([]Window, error)
}

// All returns the canonical list of supported providers: Claude, Codex, Antigravity.
func All() []Provider {
	return []Provider{
		&Claude{},
		&Codex{},
		&Antigravity{},
	}
}

// Find looks up a provider by any accepted CLI name/prefix.
func Find(name string) Provider {
	for _, p := range All() {
		for _, n := range p.Names() {
			if n == name {
				return p
			}
		}
	}
	return nil
}

// Ordered returns providers with saved accounts first, then empty providers,
// preserving the canonical Claude, Codex, Antigravity order within each group.
func Ordered(providers []Provider, counts map[string]int) []Provider {
	withAccts := make([]Provider, 0, len(providers))
	empty := make([]Provider, 0, len(providers))
	for _, p := range providers {
		if counts[p.Key()] > 0 {
			withAccts = append(withAccts, p)
		} else {
			empty = append(empty, p)
		}
	}
	return append(withAccts, empty...)
}

// OrderedForEnv loads slot counts from disk and returns providers in display order.
func OrderedForEnv(env *Env) []Provider {
	all := All()
	counts := make(map[string]int, len(all))
	for _, p := range all {
		slots, _ := p.Slots(env)
		counts[p.Key()] = len(slots)
	}
	return Ordered(all, counts)
}

// MigrateAll performs legacy folder migration (~/.claudeswap, ~/.codexswap, ~/.aswap)
// when TORI_HOME is not overridden.
func MigrateAll(env *Env) (notices []string, warnings []string) {
	all := All()
	migrations := make([]store.LegacyMigration, 0, len(all))
	for _, p := range all {
		if leg := p.LegacyDir(); leg != "" {
			migrations = append(migrations, store.LegacyMigration{
				Key:       p.Key(),
				LegacyDir: leg,
			})
		}
	}
	return store.MigrateLegacy(env.Root, env.Home, env.RootOverridden, migrations)
}

// Resolve finds a slot matching ref (slot number, case-insensitive email, or case-insensitive alias).
func Resolve(p Provider, ref string, accts []store.Slot) (store.Slot, error) {
	r := strings.ToLower(ref)
	for _, s := range accts {
		if ref == strconv.Itoa(s.Number) ||
			(s.Meta.Email != "" && r == strings.ToLower(s.Meta.Email)) ||
			(s.Meta.Alias != "" && r == strings.ToLower(s.Meta.Alias)) {
			return s, nil
		}
	}
	return store.Slot{}, Failf("no account matches '%s' (try: tori %s list)", ref, p.Short())
}

// DefaultNameOf returns the display name for a slot.
func DefaultNameOf(n int, m store.Meta) string {
	if m.Email != "" {
		return m.Email
	}
	return fmt.Sprintf("account-%d", n)
}

// DefaultLabel formats "<name> [alias] · <plan>".
func DefaultLabel(name string, m store.Meta) string {
	bits := []string{name}
	if m.Alias != "" {
		bits = append(bits, fmt.Sprintf("[%s]", m.Alias))
	}
	if m.Plan != "" && m.Plan != "API key" {
		bits = append(bits, "· "+m.Plan)
	}
	return strings.Join(bits, " ")
}

// Switch switches to ref, or rotates to the next account when ref is empty.
func Switch(env *Env, p Provider, ref string) ([]string, error) {
	accts, err := p.Slots(env)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, Failf("no accounts saved yet (%s)", p.SetupHint())
	}
	cur := p.Current(env, accts)
	var target store.Slot
	if ref != "" {
		target, err = Resolve(p, ref, accts)
		if err != nil {
			return nil, err
		}
		if cur != nil && target.Number == *cur {
			return []string{fmt.Sprintf("Already on slot %d: %s", target.Number, p.Label(target.Number, target.Meta))}, nil
		}
	} else {
		if len(accts) < 2 {
			return nil, Fail("only one account saved; add another first")
		}
		nextIdx := 0
		if cur != nil {
			for i, s := range accts {
				if s.Number == *cur {
					nextIdx = (i + 1) % len(accts)
					break
				}
			}
		}
		target = accts[nextIdx]
	}
	extra, err := p.Activate(env, target, accts, cur)
	if err != nil {
		return nil, err
	}
	out := []string{fmt.Sprintf("Switched to slot %d: %s", target.Number, p.Label(target.Number, target.Meta))}
	out = append(out, extra...)
	return out, nil
}

// Remove removes the resolved slot and compacts remaining slots to 1..N.
func Remove(env *Env, p Provider, slot store.Slot) (string, error) {
	renumbered, err := store.RemoveSlot(env.Root, p.Key(), p.SlotFile(), slot.Number)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Removed %s.", p.Label(slot.Number, slot.Meta))
	if renumbered {
		msg += " Remaining accounts were renumbered."
	}
	return msg, nil
}

// SetAlias sets or clears the alias on the resolved slot.
func SetAlias(env *Env, p Provider, ref, alias string, unset bool) (string, error) {
	accts, err := p.Slots(env)
	if err != nil {
		return "", err
	}
	slot, err := Resolve(p, ref, accts)
	if err != nil {
		return "", err
	}
	if unset {
		slot.Meta.Alias = ""
	} else {
		slot.Meta.Alias = alias
	}
	if err := store.WriteJSONAtomic(filepath.Join(slot.Dir, "meta.json"), slot.Meta, 0o600, false); err != nil {
		return "", err
	}
	return fmt.Sprintf("Slot %d: %s", slot.Number, p.Label(slot.Number, slot.Meta)), nil
}

// WindowsOf extracts windows from either the current windows array or legacy fiveHour/sevenDay fields.
func WindowsOf(cache UsageCache) []Window {
	if len(cache.Windows) > 0 {
		out := make([]Window, 0, len(cache.Windows))
		for _, w := range cache.Windows {
			if w.Label != "" {
				out = append(out, w)
			}
		}
		return out
	}
	var out []Window
	for _, pair := range []struct {
		label string
		m     map[string]any
	}{
		{"5h", cache.FiveHour},
		{"7d", cache.SevenDay},
	} {
		if pair.m == nil {
			continue
		}
		if pct, ok := toFloat(pair.m["pct"]); ok {
			out = append(out, Window{
				Label:    pair.label,
				Pct:      pct,
				ResetsAt: pair.m["resetsAt"],
			})
		}
	}
	return out
}

// UsageForSlot reads cached usage and refreshes over network when enabled and due.
// Never makes a network call when usage is disabled for p or network is false.
func UsageForSlot(env *Env, p Provider, slot store.Slot, active bool, ctx any, network, force bool) *UsageInfo {
	if p.NoUsage(slot.Meta) || !env.IsUsageEnabled(p.Key()) {
		return nil
	}
	now := float64(env.now().UnixNano()) / 1e9
	var cache UsageCache
	_ = store.ReadJSON(filepath.Join(slot.Dir, "usage.json"), &cache)
	cache = p.Preload(env, slot, cache)

	var errText string
	fresh := cache.FetchedAt > 0 && (now-cache.FetchedAt) < config.UsageTTL.Seconds()
	if network && !env.DisableNetwork && !(fresh && !force) {
		wins, err := p.Fetch(env, slot, active, ctx)
		if err == nil {
			cache = UsageCache{Windows: wins, FetchedAt: now}
			_ = store.WriteJSONAtomic(filepath.Join(slot.Dir, "usage.json"), cache, 0o600, false)
		} else {
			var he *HTTPError
			if errors.As(err, &he) {
				if he.StatusCode == 429 && cache.FetchedAt > 0 && (now-cache.FetchedAt) < config.UsageTTL.Seconds()*2 {
					errText = ""
				} else {
					errText = HTTPStatusText(he.StatusCode)
				}
			} else if errors.Is(err, ErrPerm) {
				errText = p.PermError()
			} else {
				errText = "offline"
			}
		}
	}
	if cache.FetchedAt == 0 && errText == "" {
		return nil
	}
	var fetchedPtr *float64
	var agePtr *int
	if cache.FetchedAt > 0 {
		f := cache.FetchedAt
		fetchedPtr = &f
		age := int(now - cache.FetchedAt)
		if age < 0 {
			age = 0
		}
		agePtr = &age
	}
	var errPtr *string
	if errText != "" {
		s := errText
		errPtr = &s
	}
	return &UsageInfo{
		Windows:    WindowsOf(cache),
		FetchedAt:  fetchedPtr,
		AgeSeconds: agePtr,
		Error:      errPtr,
	}
}

// UsageForAll fetches usage concurrently (max 4 workers) for all slots of p.
func UsageForAll(env *Env, p Provider, accts []store.Slot, cur *int, network, force bool) map[int]*UsageInfo {
	if !env.IsUsageEnabled(p.Key()) || len(accts) == 0 {
		return nil
	}
	ctx := p.UsageCtx(env)
	out := make(map[int]*UsageInfo, len(accts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for _, slot := range accts {
		wg.Add(1)
		go func(s store.Slot) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			active := cur != nil && s.Number == *cur
			info := UsageForSlot(env, p, s, active, ctx, network, force)
			mu.Lock()
			out[s.Number] = info
			mu.Unlock()
		}(slot)
	}
	wg.Wait()
	return out
}

// Entries builds the account entry list for CLI and TUI.
func Entries(env *Env, p Provider, network, force bool) ([]AccountEntry, *int, error) {
	accts, err := p.Slots(env)
	if err != nil {
		return nil, nil, err
	}
	cur := p.Current(env, accts)
	usage := UsageForAll(env, p, accts, cur, network, force)
	// Re-read slots in case usage fetch learned metadata (e.g., Antigravity email).
	if len(accts) > 0 && usage != nil {
		if reloaded, err := p.Slots(env); err == nil {
			accts = reloaded
		}
	}
	out := make([]AccountEntry, 0, len(accts))
	for _, s := range accts {
		name := p.NameOf(s.Number, s.Meta)
		var aliasPtr *string
		if s.Meta.Alias != "" && s.Meta.Alias != name {
			a := s.Meta.Alias
			aliasPtr = &a
		}
		var planPtr *PlanInfo
		if s.Meta.Plan != "" && s.Meta.Plan != "API key" {
			planPtr = &PlanInfo{Label: s.Meta.Plan}
		}
		active := cur != nil && s.Number == *cur
		out = append(out, AccountEntry{
			Number: s.Number,
			Email:  name,
			Alias:  aliasPtr,
			Active: active,
			Plan:   planPtr,
			Usage:  usage[s.Number],
			Note:   p.ListNote(s.Meta),
		})
	}
	return out, cur, nil
}

// HTTPJSON performs an HTTP GET (body == nil) or POST (body != nil) and decodes JSON response.
func HTTPJSON(env *Env, url string, body []byte, headers map[string]string) (map[string]any, error) {
	method := http.MethodGet
	var bodyReader io.Reader
	if body != nil {
		method = http.MethodPost
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := env.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &HTTPError{StatusCode: resp.StatusCode}
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Bar renders a simple ASCII/Unicode block meter for CLI output.
func Bar(pct float64, width int) string {
	filled := int(math.Round(float64(width) * pct / 100.0))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// UsageLine formats one-line usage text for `tori list`.
func UsageLine(u *UsageInfo, extra string) string {
	if extra != "" {
		return extra
	}
	if u == nil {
		return ""
	}
	if u.Error != nil && *u.Error != "" && len(u.Windows) == 0 {
		return *u.Error
	}
	bits := make([]string, 0, len(u.Windows)+1)
	for _, w := range u.Windows {
		bits = append(bits, fmt.Sprintf("%s %s %3.0f%%", w.Label, Bar(w.Pct, 10), w.Pct))
	}
	if u.Error != nil && *u.Error != "" {
		bits = append(bits, fmt.Sprintf("(%s)", *u.Error))
	}
	return strings.Join(bits, "   ")
}

// ResetEpoch parses resetsAt (ISO 8601 string, unix seconds, or unix milliseconds) to unix seconds.
func ResetEpoch(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if t > 1e11 {
			return t / 1000.0, true
		}
		return t, true
	case int64:
		f := float64(t)
		if f > 1e11 {
			return f / 1000.0, true
		}
		return f, true
	case int:
		f := float64(t)
		if f > 1e11 {
			return f / 1000.0, true
		}
		return f, true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			if f > 1e11 {
				return f / 1000.0, true
			}
			return f, true
		}
	case string:
		if t == "" {
			return 0, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return float64(parsed.UnixNano()) / 1e9, true
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return float64(parsed.UnixNano()) / 1e9, true
		}
	}
	return 0, false
}

// FmtCountdown formats seconds remaining until epoch.
func FmtCountdown(epoch float64, now time.Time) string {
	sec := int(epoch) - int(now.Unix())
	if sec <= 0 {
		return "now"
	}
	d := sec / 86400
	r := sec % 86400
	h := r / 3600
	r = r % 3600
	m := r / 60
	if d > 0 {
		return fmt.Sprintf("%dd%dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%dm", m)
}

// FmtAge formats cache age in seconds as "Xm ago" or "Xh ago".
func FmtAge(sec int) string {
	if sec < 3600 {
		return fmt.Sprintf("%dm ago", sec/60)
	}
	return fmt.Sprintf("%dh ago", sec/3600)
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	newWord := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if newWord {
				b.WriteRune(unicode.ToTitle(r))
				newWord = false
			} else {
				b.WriteRune(unicode.ToLower(r))
			}
		} else {
			b.WriteRune(r)
			newWord = true
		}
	}
	return b.String()
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func maxSlotNumber(accts []store.Slot) int {
	maxN := 0
	for _, s := range accts {
		if s.Number > maxN {
			maxN = s.Number
		}
	}
	sort.SliceStable(accts, func(i, j int) bool { return accts[i].Number < accts[j].Number })
	return maxN
}
