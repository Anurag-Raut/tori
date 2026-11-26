package provider

import (
	"crypto/sha256"
	"encoding/base64"
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
	"time"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	agyGoogleTokenURL = "https://oauth2.googleapis.com/token"
	agyDailyEndpoint  = "https://daily-cloudcode-pa.googleapis.com"
	agyProdEndpoint   = "https://cloudcode-pa.googleapis.com"
	agyUserinfoURL    = "https://www.googleapis.com/oauth2/v2/userinfo"
	agyTokenSkewSec   = 60.0

	agyAuthFile   = "auth.json"
	agyLegacyFile = "acp_token.json"

	agySurfaceDesktop = "desktop"
	agySurfaceIDE     = "ide"
	agySurfaceCLI     = "cli"
	agySurfaceT3      = "t3"
)

// Antigravity implements Provider for Antigravity Desktop, IDE, CLI (agy), and T3 Code.
type Antigravity struct{}

func (g *Antigravity) Key() string       { return "antigravity" }
func (g *Antigravity) Title() string     { return "Antigravity" }
func (g *Antigravity) Short() string     { return "g" }
func (g *Antigravity) Names() []string   { return []string{"g", "a", "agy", "antigravity", "gemini"} }
func (g *Antigravity) LegacyDir() string { return ".aswap" }
func (g *Antigravity) SlotFile() string  { return agyAuthFile }
func (g *Antigravity) AddHint() string   { return "tori g add <alias>" }
func (g *Antigravity) PermError() string { return "open the app to refresh" }
func (g *Antigravity) SetupHint() string {
	return "Sign in to Antigravity (Desktop, IDE, CLI `agy`, or T3 Code), then run `tori g add <alias>`."
}
func (g *Antigravity) HelpTitle() string {
	return "Antigravity accounts (Desktop, IDE, CLI `agy`, and T3 Code)"
}
func (g *Antigravity) SetupText() string {
	return "Setup: sign in to Antigravity (Desktop, IDE, CLI `agy`, or T3 Code) as account A -> `tori g add a`;\nthen sign out and in as account B -> `tori g add b`."
}

func (g *Antigravity) Slots(env *Env) ([]store.Slot, error) {
	return store.SlotsAny(env.Root, g.Key(), agyAuthFile, agyLegacyFile)
}

func (g *Antigravity) Compact(env *Env) (bool, error) {
	return store.CompactAny(env.Root, g.Key(), agyAuthFile, agyLegacyFile)
}

func (g *Antigravity) NoUsage(store.Meta) bool    { return false }
func (g *Antigravity) ListNote(store.Meta) string { return "" }

// agyCLISnap stores the raw credential JSON for Antigravity CLI (agy) and Desktop 2.x.
type agyCLISnap struct {
	RawJSON string `json:"rawJson,omitempty"`
}

// agyT3Snap stores the raw credential file for T3 Code's Antigravity harness.
type agyT3Snap struct {
	Format  string `json:"format,omitempty"` // "acp" or "oauth"
	RawJSON string `json:"rawJson,omitempty"`
}

// agySlotAuth is the canonical per-slot credential file stored at <slot>/auth.json.
type agySlotAuth struct {
	RefreshToken string `json:"refreshToken,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	Expiry       int64  `json:"expiry,omitempty"`
	Email        string `json:"email,omitempty"`
	Name         string `json:"name,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	TokenURI     string `json:"tokenUri,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`

	Desktop *agyStateDBSnap `json:"desktop,omitempty"`
	IDE     *agyStateDBSnap `json:"ide,omitempty"`
	CLI     *agyCLISnap     `json:"cli,omitempty"`
	T3      *agyT3Snap      `json:"t3,omitempty"`
}

// agyLiveLogin describes one live login detected on an installed Antigravity surface.
type agyLiveLogin struct {
	Surface      string // desktop, ide, cli, t3
	RefreshToken string
	AccessToken  string
	Expiry       int64
	Email        string
	Name         string
	ClientID     string
	ClientSecret string
	TokenURI     string
	ProjectID    string
	AuthMethod   string
	ModTime      time.Time

	Desktop *agyStateDBSnap
	IDE     *agyStateDBSnap
	CLI     *agyCLISnap
	T3      *agyT3Snap
}

func agySurfaceTitle(surface string) string {
	switch surface {
	case agySurfaceDesktop:
		return "Antigravity Desktop"
	case agySurfaceIDE:
		return "Antigravity IDE"
	case agySurfaceCLI:
		return "Antigravity CLI"
	case agySurfaceT3:
		return "T3 Code"
	default:
		return "Antigravity"
	}
}

func agySurfacePriority(surface string) int {
	switch surface {
	case agySurfaceDesktop:
		return 0
	case agySurfaceIDE:
		return 1
	case agySurfaceCLI:
		return 2
	case agySurfaceT3:
		return 3
	default:
		return 4
	}
}

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

func (g *Antigravity) osConfigBase(env *Env) string {
	switch env.GOOS {
	case "darwin":
		if env.Home == "" {
			return ""
		}
		return filepath.Join(env.Home, "Library", "Application Support")
	case "windows":
		if appData := env.getenv("APPDATA"); appData != "" {
			return appData
		}
		if env.Home == "" {
			return ""
		}
		return filepath.Join(env.Home, "AppData", "Roaming")
	default:
		if xdg := env.getenv("XDG_CONFIG_HOME"); xdg != "" {
			return xdg
		}
		if env.Home == "" {
			return ""
		}
		return filepath.Join(env.Home, ".config")
	}
}

func (g *Antigravity) desktopDBPath(env *Env) (path string, installed bool) {
	if v := env.getenv("TORI_AGY_DESKTOP_DB"); v != "" {
		return v, fileExists(v) || dirExists(filepath.Dir(v))
	}
	base := g.osConfigBase(env)
	if base == "" {
		return "", false
	}
	appDir := filepath.Join(base, "Antigravity")
	db := filepath.Join(appDir, "User", "globalStorage", "state.vscdb")
	if fileExists(db) {
		return db, true
	}
	return db, dirExists(appDir)
}

func (g *Antigravity) ideDBPath(env *Env) (path string, installed bool) {
	if v := env.getenv("TORI_AGY_IDE_DB"); v != "" {
		return v, fileExists(v) || dirExists(filepath.Dir(v))
	}
	base := g.osConfigBase(env)
	if base == "" {
		return "", false
	}
	for _, folder := range []string{"Antigravity IDE", "AntigravityIDE"} {
		appDir := filepath.Join(base, folder)
		db := filepath.Join(appDir, "User", "globalStorage", "state.vscdb")
		if fileExists(db) {
			return db, true
		}
		if dirExists(appDir) {
			return db, true
		}
	}
	return filepath.Join(base, "Antigravity IDE", "User", "globalStorage", "state.vscdb"), false
}

func (g *Antigravity) cliTokenPaths(env *Env) (cliFile, desktop2File string, installed bool) {
	if v := env.getenv("TORI_AGY_CLI_TOKEN"); v != "" {
		cliFile = v
		if fileExists(v) || dirExists(filepath.Dir(v)) {
			installed = true
		}
	} else if env.Home != "" {
		cliFile = filepath.Join(env.Home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		if fileExists(cliFile) || dirExists(filepath.Dir(cliFile)) {
			installed = true
		}
	}

	if v := env.getenv("TORI_AGY_DESKTOP_TOKEN"); v != "" {
		desktop2File = v
		if fileExists(v) || dirExists(filepath.Dir(v)) {
			installed = true
		}
	} else if env.Home != "" {
		desktop2File = filepath.Join(env.Home, ".gemini", "jetski-standalone-oauth-token")
		if fileExists(desktop2File) || dirExists(filepath.Join(env.Home, ".gemini", "antigravity")) {
			installed = true
		}
	}

	if !installed && env.LookPath != nil {
		if _, err := env.LookPath("agy"); err == nil {
			installed = true
		}
	}
	if !installed && env.Home != "" {
		binName := "agy"
		if env.GOOS == "windows" {
			binName = "agy.exe"
		}
		for _, p := range []string{
			filepath.Join(env.Home, ".local", "bin", binName),
			filepath.Join(env.Home, ".gemini", "antigravity-cli", "bin", binName),
		} {
			if fileExists(p) {
				installed = true
				break
			}
		}
	}
	return cliFile, desktop2File, installed
}

func (g *Antigravity) t3TokenPaths(env *Env) (paths []string, defaultDir string, installed bool) {
	if v := env.getenv("TORI_AGY_TOKEN"); v != "" {
		return []string{v}, filepath.Dir(v), true
	}
	if env.Home == "" {
		return nil, "", false
	}
	dirPattern := filepath.Join(env.Home, ".t3", "userdata", "providers", "antigravity", "*", "antigravity-acp")
	dirs, err := filepath.Glob(dirPattern)
	if err != nil || len(dirs) == 0 {
		return nil, "", false
	}
	sort.Strings(dirs)
	defaultDir = dirs[0]
	for _, d := range dirs {
		for _, name := range []string{"antigravity-oauth-token", "acp_token.json"} {
			p := filepath.Join(d, name)
			if fileExists(p) {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		paths = append(paths, filepath.Join(defaultDir, "acp_token.json"))
	}
	return paths, defaultDir, true
}

func parseAgyTokenJSON(raw []byte) (refresh, access string, expiry int64, email, clientID, clientSecret, tokenURI, projectID, authMethod, format string) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return
	}
	// Format 1: nested {"token": {"access_token", "refresh_token", "expiry"}, "auth_method", "id_token", ...}
	if tok := getMap(m, "token"); len(tok) > 0 {
		format = "oauth"
		refresh = getString(tok, "refresh_token")
		access = getString(tok, "access_token")
		if expStr := getString(tok, "expiry"); expStr != "" {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000Z", time.RFC3339} {
				if t, err := time.Parse(layout, expStr); err == nil {
					expiry = t.Unix()
					break
				}
			}
		}
		authMethod = getString(m, "auth_method")
		projectID = getString(m, "project_id")
		email = strings.TrimSpace(getString(m, "id_token_email"))
		if email == "" {
			if idt := getString(m, "id_token"); idt != "" {
				email = strings.TrimSpace(getString(agyJWTPayload(idt), "email"))
			}
		}
		return
	}
	// Format 2: flat ACP JSON {"client_id", "client_secret", "refresh_token", "token_uri", "project_id"}
	format = "acp"
	refresh = getString(m, "refresh_token")
	access = getString(m, "access_token")
	clientID = getString(m, "client_id")
	clientSecret = getString(m, "client_secret")
	tokenURI = getString(m, "token_uri")
	projectID = getString(m, "project_id")
	email = strings.TrimSpace(getString(m, "email"))
	return
}

func agyJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func (g *Antigravity) probeLive(env *Env) (live []agyLiveLogin, anyInstalled bool) {
	// 1. Antigravity Desktop / Classic (1.x) via state.vscdb
	if dbPath, inst := g.desktopDBPath(env); inst {
		anyInstalled = true
		if fileExists(dbPath) {
			if snap, at, rt, exp, email, name, err := readStateDBAuth(dbPath); err == nil && snap != nil && rt != "" {
				live = append(live, agyLiveLogin{
					Surface:      agySurfaceDesktop,
					RefreshToken: rt,
					AccessToken:  at,
					Expiry:       exp,
					Email:        email,
					Name:         name,
					AuthMethod:   "consumer",
					ModTime:      fileModTime(dbPath),
					Desktop:      snap,
				})
			}
		}
	}

	// 2. Antigravity IDE via state.vscdb
	if dbPath, inst := g.ideDBPath(env); inst {
		anyInstalled = true
		if fileExists(dbPath) {
			if snap, at, rt, exp, email, name, err := readStateDBAuth(dbPath); err == nil && snap != nil && rt != "" {
				live = append(live, agyLiveLogin{
					Surface:      agySurfaceIDE,
					RefreshToken: rt,
					AccessToken:  at,
					Expiry:       exp,
					Email:        email,
					Name:         name,
					AuthMethod:   "consumer",
					ModTime:      fileModTime(dbPath),
					IDE:          snap,
				})
			}
		}
	}

	// 3. Antigravity CLI (agy) & Desktop 2.x via OS keyring + file fallbacks
	cliFile, desktop2File, cliInst := g.cliTokenPaths(env)
	if cliInst {
		anyInstalled = true
	}
	var cliRaw []byte
	var cliMod time.Time
	if env.Keyring != nil {
		if secret, err := env.Keyring.Get(agyKeyringService, agyKeyringUser); err == nil && strings.TrimSpace(secret) != "" {
			anyInstalled = true
			cliRaw = []byte(strings.TrimSpace(secret))
		}
	}
	if len(cliRaw) == 0 && cliFile != "" && fileExists(cliFile) {
		if b, err := os.ReadFile(cliFile); err == nil && len(bytesTrimSpace(b)) > 0 {
			cliRaw = bytesTrimSpace(b)
			cliMod = fileModTime(cliFile)
		}
	}
	if len(cliRaw) == 0 && desktop2File != "" && fileExists(desktop2File) {
		if b, err := os.ReadFile(desktop2File); err == nil && len(bytesTrimSpace(b)) > 0 {
			cliRaw = bytesTrimSpace(b)
			cliMod = fileModTime(desktop2File)
		}
	}
	if len(cliRaw) > 0 {
		rt, at, exp, email, cid, csec, tokURI, projID, authMethod, _ := parseAgyTokenJSON(cliRaw)
		if rt != "" {
			if authMethod == "" {
				authMethod = "consumer"
			}
			live = append(live, agyLiveLogin{
				Surface:      agySurfaceCLI,
				RefreshToken: rt,
				AccessToken:  at,
				Expiry:       exp,
				Email:        email,
				ClientID:     cid,
				ClientSecret: csec,
				TokenURI:     tokURI,
				ProjectID:    projID,
				AuthMethod:   authMethod,
				ModTime:      cliMod,
				CLI:          &agyCLISnap{RawJSON: string(cliRaw)},
			})
		}
	}

	// 4. T3 Code (acp_token.json or antigravity-oauth-token)
	if t3Paths, _, t3Inst := g.t3TokenPaths(env); t3Inst {
		anyInstalled = true
		var best *agyLiveLogin
		for _, p := range t3Paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			rt, at, exp, email, cid, csec, tokURI, projID, authMethod, format := parseAgyTokenJSON(raw)
			if rt == "" {
				continue
			}
			cand := &agyLiveLogin{
				Surface:      agySurfaceT3,
				RefreshToken: rt,
				AccessToken:  at,
				Expiry:       exp,
				Email:        email,
				ClientID:     cid,
				ClientSecret: csec,
				TokenURI:     tokURI,
				ProjectID:    projID,
				AuthMethod:   authMethod,
				ModTime:      fileModTime(p),
				T3: &agyT3Snap{
					Format:  format,
					RawJSON: string(raw),
				},
			}
			if best == nil || cand.ModTime.After(best.ModTime) {
				best = cand
			}
		}
		if best != nil {
			live = append(live, *best)
		}
	}

	return live, anyInstalled
}

func (g *Antigravity) readSlotAuth(slotDir string) *agySlotAuth {
	var auth agySlotAuth
	if store.ReadJSON(filepath.Join(slotDir, agyAuthFile), &auth) {
		if auth.RefreshToken != "" || auth.Desktop != nil || auth.IDE != nil || auth.CLI != nil || auth.T3 != nil {
			return &auth
		}
	}
	// Legacy v0.1/v0.2 slot: acp_token.json only.
	raw, err := os.ReadFile(filepath.Join(slotDir, agyLegacyFile))
	if err != nil {
		return nil
	}
	rt, at, exp, email, cid, csec, tokURI, projID, authMethod, format := parseAgyTokenJSON(raw)
	if rt == "" {
		return nil
	}
	return &agySlotAuth{
		RefreshToken: rt,
		AccessToken:  at,
		Expiry:       exp,
		Email:        email,
		ClientID:     cid,
		ClientSecret: csec,
		TokenURI:     tokURI,
		ProjectID:    projID,
		AuthMethod:   authMethod,
		T3: &agyT3Snap{
			Format:  format,
			RawJSON: string(raw),
		},
	}
}

func (a *agySlotAuth) refreshTokenForSurface(surface string) string {
	if a == nil {
		return ""
	}
	switch surface {
	case agySurfaceDesktop:
		if a.Desktop != nil && a.Desktop.OAuthToken != "" {
			if _, rt, _, err := extractOAuthFromUnified(a.Desktop.OAuthToken); err == nil && rt != "" {
				return rt
			}
		}
	case agySurfaceIDE:
		if a.IDE != nil && a.IDE.OAuthToken != "" {
			if _, rt, _, err := extractOAuthFromUnified(a.IDE.OAuthToken); err == nil && rt != "" {
				return rt
			}
		}
	case agySurfaceCLI:
		if a.CLI != nil && a.CLI.RawJSON != "" {
			rt, _, _, _, _, _, _, _, _, _ := parseAgyTokenJSON([]byte(a.CLI.RawJSON))
			if rt != "" {
				return rt
			}
		}
	case agySurfaceT3:
		if a.T3 != nil && a.T3.RawJSON != "" {
			rt, _, _, _, _, _, _, _, _, _ := parseAgyTokenJSON([]byte(a.T3.RawJSON))
			if rt != "" {
				return rt
			}
		}
	}
	return a.RefreshToken
}

func (a *agySlotAuth) hasSurface(surface string) bool {
	if a == nil {
		return false
	}
	switch surface {
	case agySurfaceDesktop:
		return a.Desktop != nil
	case agySurfaceIDE:
		return a.IDE != nil
	case agySurfaceCLI:
		return a.CLI != nil
	case agySurfaceT3:
		return a.T3 != nil
	default:
		return false
	}
}

func (a *agySlotAuth) mergeLive(l agyLiveLogin) {
	if l.RefreshToken != "" && a.RefreshToken == "" {
		a.RefreshToken = l.RefreshToken
	}
	if l.AccessToken != "" && (a.AccessToken == "" || l.Expiry > a.Expiry) {
		a.AccessToken = l.AccessToken
		a.Expiry = l.Expiry
	}
	if l.Email != "" && a.Email == "" {
		a.Email = l.Email
	}
	if l.Name != "" && a.Name == "" {
		a.Name = l.Name
	}
	if l.ClientID != "" && a.ClientID == "" {
		a.ClientID = l.ClientID
	}
	if l.ClientSecret != "" && a.ClientSecret == "" {
		a.ClientSecret = l.ClientSecret
	}
	if l.TokenURI != "" && a.TokenURI == "" {
		a.TokenURI = l.TokenURI
	}
	if l.ProjectID != "" && a.ProjectID == "" {
		a.ProjectID = l.ProjectID
	}
	if l.AuthMethod != "" && a.AuthMethod == "" {
		a.AuthMethod = l.AuthMethod
	}
	if l.Desktop != nil {
		a.Desktop = l.Desktop
	}
	if l.IDE != nil {
		a.IDE = l.IDE
	}
	if l.CLI != nil {
		a.CLI = l.CLI
	}
	if l.T3 != nil {
		a.T3 = l.T3
	}
}

func (g *Antigravity) writeSlotAuth(slotDir string, auth *agySlotAuth) error {
	if err := store.WriteJSONAtomic(filepath.Join(slotDir, agyAuthFile), auth, 0o600, false); err != nil {
		return err
	}
	// Preserve legacy acp_token.json alongside auth.json when a T3 ACP snapshot is present.
	if auth.T3 != nil && auth.T3.Format == "acp" && auth.T3.RawJSON != "" {
		if err := store.WriteBytesAtomic(filepath.Join(slotDir, agyLegacyFile), []byte(auth.T3.RawJSON), 0o600, false); err != nil {
			return err
		}
	}
	return nil
}

func (g *Antigravity) matchLiveSlot(live agyLiveLogin, accts []store.Slot) (slot *store.Slot, exactSurface bool) {
	// 1. Exact refresh token match on the same surface or top-level token.
	for i := range accts {
		auth := g.readSlotAuth(accts[i].Dir)
		if auth == nil {
			continue
		}
		if live.RefreshToken != "" && (auth.refreshTokenForSurface(live.Surface) == live.RefreshToken || auth.RefreshToken == live.RefreshToken) {
			return &accts[i], auth.hasSurface(live.Surface)
		}
	}
	// 2. Fallback: case-insensitive email match (handles rotated refresh tokens or adding a second surface for an existing account).
	if live.Email != "" {
		var emailMatch *store.Slot
		matchCount := 0
		for i := range accts {
			auth := g.readSlotAuth(accts[i].Dir)
			slotEmail := accts[i].Meta.Email
			if slotEmail == "" && auth != nil {
				slotEmail = auth.Email
			}
			if slotEmail != "" && strings.EqualFold(slotEmail, live.Email) {
				if auth != nil && auth.hasSurface(live.Surface) {
					return &accts[i], true
				}
				emailMatch = &accts[i]
				matchCount++
			}
		}
		if matchCount == 1 {
			return emailMatch, false
		}
	}
	return nil, false
}

func pickPrimaryLive(items []agyLiveLogin) agyLiveLogin {
	best := items[0]
	for _, it := range items[1:] {
		if !it.ModTime.IsZero() && !best.ModTime.IsZero() && !it.ModTime.Equal(best.ModTime) {
			if it.ModTime.After(best.ModTime) {
				best = it
			}
			continue
		}
		if agySurfacePriority(it.Surface) < agySurfacePriority(best.Surface) {
			best = it
		}
	}
	return best
}

func sameAccountLive(a, b agyLiveLogin) bool {
	if a.RefreshToken != "" && b.RefreshToken != "" && a.RefreshToken == b.RefreshToken {
		return true
	}
	if a.Email != "" && b.Email != "" && strings.EqualFold(a.Email, b.Email) {
		return true
	}
	return false
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
	live, _ := g.probeLive(env)
	if len(live) == 0 {
		return nil
	}
	type matched struct {
		live agyLiveLogin
		slot int
	}
	var matches []matched
	for _, l := range live {
		if s, _ := g.matchLiveSlot(l, accts); s != nil {
			matches = append(matches, matched{live: l, slot: s.Number})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	best := matches[0]
	for _, m := range matches[1:] {
		if !m.live.ModTime.IsZero() && !best.live.ModTime.IsZero() && !m.live.ModTime.Equal(best.live.ModTime) {
			if m.live.ModTime.After(best.live.ModTime) {
				best = m
			}
			continue
		}
		if agySurfacePriority(m.live.Surface) < agySurfacePriority(best.live.Surface) {
			best = m
		}
	}
	n := best.slot
	return &n
}

// ClearLive removes local Antigravity credentials across all signed-in surfaces without
// revoking server-side Google OAuth tokens. The returned restore func rewrites all backed-up state.
func (g *Antigravity) ClearLive(env *Env) (func() error, error) {
	live, _ := g.probeLive(env)
	if len(live) == 0 {
		return nil, nil
	}
	// Guard running GUI apps before mutating state.vscdb.
	for _, l := range live {
		if (l.Surface == agySurfaceDesktop || l.Surface == agySurfaceIDE) && env.ProcessRunning != nil && env.ProcessRunning(l.Surface) {
			return nil, Failf("%s is running; close it before clearing its login.", agySurfaceTitle(l.Surface))
		}
	}

	var restores []func() error
	rollback := func() {
		for i := len(restores) - 1; i >= 0; i-- {
			_ = restores[i]()
		}
	}

	for _, l := range live {
		switch l.Surface {
		case agySurfaceDesktop:
			dbPath, _ := g.desktopDBPath(env)
			r, err := clearStateDBAuth(dbPath)
			if err != nil {
				rollback()
				return nil, err
			}
			if r != nil {
				restores = append(restores, r)
			}
		case agySurfaceIDE:
			dbPath, _ := g.ideDBPath(env)
			r, err := clearStateDBAuth(dbPath)
			if err != nil {
				rollback()
				return nil, err
			}
			if r != nil {
				restores = append(restores, r)
			}
		case agySurfaceCLI:
			if env.Keyring != nil {
				if prev, err := env.Keyring.Get(agyKeyringService, agyKeyringUser); err == nil && prev != "" {
					if err := env.Keyring.Delete(agyKeyringService, agyKeyringUser); err != nil {
						rollback()
						return nil, err
					}
					kr := env.Keyring
					prevCopy := prev
					restores = append(restores, func() error {
						return kr.Set(agyKeyringService, agyKeyringUser, prevCopy)
					})
				}
			}
			cliFile, desktop2File, _ := g.cliTokenPaths(env)
			for _, p := range []string{cliFile, desktop2File} {
				if p == "" || !fileExists(p) {
					continue
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					rollback()
					return nil, err
				}
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					rollback()
					return nil, err
				}
				pathCopy, rawCopy := p, raw
				restores = append(restores, func() error {
					return store.WriteBytesAtomic(pathCopy, rawCopy, 0o600, false)
				})
			}
		case agySurfaceT3:
			t3Paths, _, _ := g.t3TokenPaths(env)
			for _, p := range t3Paths {
				if !fileExists(p) {
					continue
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					rollback()
					return nil, err
				}
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					rollback()
					return nil, err
				}
				pathCopy, rawCopy := p, raw
				restores = append(restores, func() error {
					return store.WriteBytesAtomic(pathCopy, rawCopy, 0o600, false)
				})
			}
		}
	}

	if len(restores) == 0 {
		return nil, nil
	}
	return func() error {
		var firstErr error
		for i := len(restores) - 1; i >= 0; i-- {
			if err := restores[i](); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}, nil
}

func (g *Antigravity) Status(env *Env) (StatusInfo, error) {
	accts, _ := g.Slots(env)
	live, anyInstalled := g.probeLive(env)
	if len(live) == 0 {
		if !anyInstalled {
			return StatusInfo{
				LoggedIn: false,
				Message:  "Antigravity not found (checked Antigravity Desktop/IDE, CLI `agy`, and T3 Code).",
			}, nil
		}
		return StatusInfo{
			LoggedIn: false,
			Message:  "Not logged in to Antigravity.",
		}, nil
	}

	// Prefer surfacing any unsaved live login so `tori g status` and TUI `a` detect new sign-ins immediately.
	var unsaved []agyLiveLogin
	for _, l := range live {
		if s, exact := g.matchLiveSlot(l, accts); s == nil || !exact {
			unsaved = append(unsaved, l)
		}
	}
	if len(unsaved) > 0 {
		primary := pickPrimaryLive(unsaved)
		disp := primary.Email
		if disp == "" {
			disp = "signed-in account"
		}
		msg := fmt.Sprintf("Live login: %s (%s), not saved (run `tori g add <alias>`).", disp, agySurfaceTitle(primary.Surface))
		if primary.Email == "" && primary.Surface == agySurfaceT3 {
			msg = "Live login: present, not saved (run `tori g add <alias>`)."
		}
		return StatusInfo{
			LoggedIn:    true,
			DisplayName: disp,
			Message:     msg,
		}, nil
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
	live, anyInstalled := g.probeLive(env)
	if len(live) == 0 {
		if !anyInstalled {
			return nil, Fail("no Antigravity installation found (checked Antigravity Desktop/IDE, CLI `agy`, and T3 Code). Install Antigravity and sign in first.")
		}
		return nil, Fail("no Antigravity login found. Sign in to Antigravity Desktop/IDE, Antigravity CLI (`agy`), or T3 Code first.")
	}
	accts, err := g.Slots(env)
	if err != nil {
		return nil, err
	}

	// Separate truly new accounts from existing-account updates / additional surfaces.
	var unsaved []agyLiveLogin
	var attachSurface []struct {
		live agyLiveLogin
		slot store.Slot
	}
	for _, l := range live {
		s, exact := g.matchLiveSlot(l, accts)
		if s == nil {
			unsaved = append(unsaved, l)
		} else if !exact {
			attachSurface = append(attachSurface, struct {
				live agyLiveLogin
				slot store.Slot
			}{live: l, slot: *s})
		}
	}

	// Case 1: Brand-new unsaved account detected on one or more surfaces.
	if len(unsaved) > 0 {
		primary := pickPrimaryLive(unsaved)
		auth := &agySlotAuth{}
		auth.mergeLive(primary)
		for _, other := range unsaved {
			if other.Surface != primary.Surface && sameAccountLive(primary, other) {
				auth.mergeLive(other)
			}
		}
		n := maxSlotNumber(accts) + 1
		dir := store.SlotDir(env.Root, g.Key(), n)
		if err := g.writeSlotAuth(dir, auth); err != nil {
			return nil, err
		}
		meta := store.Meta{
			Email: auth.Email,
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

	// Case 2: Same email signed in on an additional surface -> merge into existing slot.
	if len(attachSurface) > 0 {
		target := attachSurface[0]
		auth := g.readSlotAuth(target.slot.Dir)
		if auth == nil {
			auth = &agySlotAuth{}
		}
		var addedSurfaces []string
		for _, item := range attachSurface {
			if item.slot.Number != target.slot.Number {
				continue
			}
			auth.mergeLive(item.live)
			addedSurfaces = append(addedSurfaces, agySurfaceTitle(item.live.Surface))
		}
		if err := g.writeSlotAuth(target.slot.Dir, auth); err != nil {
			return nil, err
		}
		meta := target.slot.Meta
		if meta.Email == "" && auth.Email != "" {
			meta.Email = auth.Email
		}
		if len(args) > 0 && args[0] != "" {
			meta.Alias = args[0]
		}
		if err := store.WriteJSONAtomic(filepath.Join(target.slot.Dir, "meta.json"), meta, 0o600, false); err != nil {
			return nil, err
		}
		ClearUsageCooldown(target.slot.Dir)
		return []string{fmt.Sprintf("Updated slot %d: %s (added %s)", target.slot.Number, g.Label(target.slot.Number, meta), strings.Join(addedSurfaces, ", "))}, nil
	}

	// Case 3: All live logins are already saved; refresh snapshot on the primary matched slot.
	primary := pickPrimaryLive(live)
	matchedSlot, _ := g.matchLiveSlot(primary, accts)
	if matchedSlot == nil {
		return nil, Fail("could not resolve current Antigravity account")
	}
	if auth := g.readSlotAuth(matchedSlot.Dir); auth != nil {
		auth.mergeLive(primary)
		_ = g.writeSlotAuth(matchedSlot.Dir, auth)
		if matchedSlot.Meta.Email == "" && auth.Email != "" {
			matchedSlot.Meta.Email = auth.Email
			_ = store.WriteJSONAtomic(filepath.Join(matchedSlot.Dir, "meta.json"), matchedSlot.Meta, 0o600, false)
		}
	}
	if len(args) > 0 && args[0] != "" {
		matchedSlot.Meta.Alias = args[0]
		if err := store.WriteJSONAtomic(filepath.Join(matchedSlot.Dir, "meta.json"), matchedSlot.Meta, 0o600, false); err != nil {
			return nil, err
		}
	}
	ClearUsageCooldown(matchedSlot.Dir)
	return []string{fmt.Sprintf("Already saved as slot %d: %s", matchedSlot.Number, g.Label(matchedSlot.Number, matchedSlot.Meta))}, nil
}

func (g *Antigravity) Activate(env *Env, slot store.Slot, _ []store.Slot, cur *int) ([]string, error) {
	auth := g.readSlotAuth(slot.Dir)
	if auth == nil {
		return nil, Failf("slot %d is empty or corrupt; remove it and add it again.", slot.Number)
	}

	// Determine target surfaces captured in this slot that exist on this machine.
	type targetPlan struct {
		surface      string
		desktopDB    string
		ideDB        string
		cliFile      string
		desktop2File string
		t3File       string
	}
	var plans []targetPlan
	var missingSurfaces []string

	if auth.Desktop != nil {
		db, inst := g.desktopDBPath(env)
		if inst || env.getenv("TORI_AGY_DESKTOP_DB") != "" {
			plans = append(plans, targetPlan{surface: agySurfaceDesktop, desktopDB: db})
		} else {
			missingSurfaces = append(missingSurfaces, agySurfaceTitle(agySurfaceDesktop))
		}
	}
	if auth.IDE != nil {
		db, inst := g.ideDBPath(env)
		if inst || env.getenv("TORI_AGY_IDE_DB") != "" {
			plans = append(plans, targetPlan{surface: agySurfaceIDE, ideDB: db})
		} else {
			missingSurfaces = append(missingSurfaces, agySurfaceTitle(agySurfaceIDE))
		}
	}
	if auth.CLI != nil {
		cliFile, desktop2File, inst := g.cliTokenPaths(env)
		if inst || env.Keyring != nil {
			plans = append(plans, targetPlan{surface: agySurfaceCLI, cliFile: cliFile, desktop2File: desktop2File})
		} else {
			missingSurfaces = append(missingSurfaces, agySurfaceTitle(agySurfaceCLI))
		}
	}
	if auth.T3 != nil {
		t3Paths, defaultDir, inst := g.t3TokenPaths(env)
		if inst {
			target := ""
			if v := env.getenv("TORI_AGY_TOKEN"); v != "" {
				target = v
			} else if defaultDir != "" {
				if auth.T3.Format == "oauth" {
					target = filepath.Join(defaultDir, "antigravity-oauth-token")
				} else {
					target = filepath.Join(defaultDir, "acp_token.json")
				}
			} else if len(t3Paths) > 0 {
				target = t3Paths[0]
			}
			plans = append(plans, targetPlan{surface: agySurfaceT3, t3File: target})
		} else {
			missingSurfaces = append(missingSurfaces, agySurfaceTitle(agySurfaceT3))
		}
	}

	if len(plans) == 0 {
		if len(missingSurfaces) > 0 {
			return nil, Failf("slot %d was saved from %s, which isn't installed here.", slot.Number, strings.Join(missingSurfaces, ", "))
		}
		return nil, Failf("slot %d has no saved Antigravity credentials; remove it and add it again.", slot.Number)
	}

	// Pre-flight: check running GUI apps BEFORE mutating any surface.
	for _, p := range plans {
		if (p.surface == agySurfaceDesktop || p.surface == agySurfaceIDE) && env.ProcessRunning != nil && env.ProcessRunning(p.surface) {
			return nil, Failf("%s is running; close it before switching accounts.", agySurfaceTitle(p.surface))
		}
	}

	// Warn if overwriting an unsaved live login.
	if cur == nil {
		if live, _ := g.probeLive(env); len(live) > 0 {
			env.warn("the current login isn't saved; run `tori g add <alias>` first to keep it.")
		}
	}

	var switched []string
	for _, p := range plans {
		switch p.surface {
		case agySurfaceDesktop:
			if err := writeStateDBAuth(p.desktopDB, auth.Desktop); err != nil {
				return nil, fmt.Errorf("update Antigravity Desktop login: %w", err)
			}
			switched = append(switched, agySurfaceDesktop)

		case agySurfaceIDE:
			if err := writeStateDBAuth(p.ideDB, auth.IDE); err != nil {
				return nil, fmt.Errorf("update Antigravity IDE login: %w", err)
			}
			switched = append(switched, agySurfaceIDE)

		case agySurfaceCLI:
			if err := g.writeCLIToken(env, auth.CLI.RawJSON, p.cliFile, p.desktop2File); err != nil {
				return nil, err
			}
			switched = append(switched, agySurfaceCLI)

		case agySurfaceT3:
			if err := store.WriteBytesAtomic(p.t3File, []byte(auth.T3.RawJSON), 0o600, false); err != nil {
				return nil, err
			}
			switched = append(switched, agySurfaceT3)
		}
	}

	if len(switched) == 1 {
		switch switched[0] {
		case agySurfaceT3:
			return []string{"Restart the T3 Antigravity session to pick it up."}, nil
		case agySurfaceDesktop:
			return []string{"Open Antigravity Desktop to pick it up."}, nil
		case agySurfaceIDE:
			return []string{"Open Antigravity IDE to pick it up."}, nil
		case agySurfaceCLI:
			return []string{"Restart `agy` (or Antigravity) to pick it up."}, nil
		}
	}
	return []string{"Restart Antigravity to pick it up."}, nil
}

func (g *Antigravity) writeCLIToken(env *Env, rawJSON, cliFile, desktop2File string) error {
	var keyringErr error
	wroteKeyring := false
	if env.Keyring != nil {
		if err := env.Keyring.Set(agyKeyringService, agyKeyringUser, rawJSON); err != nil {
			keyringErr = err
		} else {
			wroteKeyring = true
		}
	}

	wroteFile := false
	var fileErr error
	if cliFile != "" && (env.getenv("TORI_AGY_CLI_TOKEN") != "" || fileExists(cliFile) || dirExists(filepath.Dir(cliFile)) || !wroteKeyring) {
		if err := store.WriteBytesAtomic(cliFile, []byte(rawJSON), 0o600, false); err != nil {
			fileErr = err
		} else {
			wroteFile = true
		}
	}
	if desktop2File != "" && (env.getenv("TORI_AGY_DESKTOP_TOKEN") != "" || fileExists(desktop2File) || (env.Home != "" && dirExists(filepath.Join(env.Home, ".gemini", "antigravity")))) {
		if err := store.WriteBytesAtomic(desktop2File, []byte(rawJSON), 0o600, false); err == nil {
			wroteFile = true
		}
	}
	if !wroteKeyring && !wroteFile {
		if fileErr != nil {
			return fileErr
		}
		if keyringErr != nil {
			return keyringErr
		}
		return Fail("could not write Antigravity CLI credentials")
	}

	// Synchronize agy settings.json GCP block when CLI settings directory exists.
	if cliFile != "" {
		g.syncCLISettings(filepath.Join(filepath.Dir(cliFile), "settings.json"), []byte(rawJSON))
	}
	return nil
}

func (g *Antigravity) syncCLISettings(settingsPath string, rawToken []byte) {
	if !fileExists(settingsPath) && !dirExists(filepath.Dir(settingsPath)) {
		return
	}
	var tokDoc map[string]any
	if err := json.Unmarshal(rawToken, &tokDoc); err != nil {
		return
	}
	method := strings.ToLower(strings.TrimSpace(getString(tokDoc, "auth_method")))
	if method != "gcp" && method != "consumer" {
		return
	}
	settings := store.ReadJSONMap(settingsPath)
	if settings == nil {
		if method == "consumer" {
			return
		}
		settings = map[string]any{}
	}
	if method == "gcp" {
		proj := strings.TrimSpace(getString(tokDoc, "project_id"))
		if proj == "" {
			return
		}
		loc := strings.TrimSpace(getString(tokDoc, "region"))
		if loc == "" {
			loc = "global"
		}
		settings["gcp"] = map[string]any{
			"project":  proj,
			"location": loc,
		}
	} else {
		if _, had := settings["gcp"]; !had {
			return
		}
		delete(settings, "gcp")
	}
	_ = store.WriteJSONAtomic(settingsPath, settings, 0o600, true)
}

func (g *Antigravity) UsageCtx(_ *Env) any { return nil }
func (g *Antigravity) Preload(_ *Env, _ store.Slot, cache UsageCache) UsageCache {
	return cache
}

func agyFingerprint(clientID, refreshToken, projectID string) string {
	raw := clientID + "\x00" + refreshToken + "\x00" + projectID
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (g *Antigravity) accessToken(env *Env, slotDir string, auth *agySlotAuth, skipEmbedded bool) (string, error) {
	now := float64(env.now().UnixNano()) / 1e9
	clientID, clientSecret := auth.ClientID, auth.ClientSecret
	if clientID == "" || clientSecret == "" {
		if cid, csec, ok := discoverAgyOAuthClient(env, strings.EqualFold(auth.AuthMethod, "gcp")); ok {
			clientID, clientSecret = cid, csec
		}
	}

	fp := agyFingerprint(clientID, auth.RefreshToken, auth.ProjectID)
	cached := store.ReadJSONMap(filepath.Join(slotDir, "access.json"))
	if cached != nil && getString(cached, "fingerprint") == fp {
		if at := getString(cached, "accessToken"); at != "" {
			if exp, ok := toFloat(cached["expiresAt"]); ok && exp-agyTokenSkewSec > now {
				return at, nil
			}
		}
	}

	// Native logins (Desktop/IDE/CLI) already carry an access token + expiry.
	if !skipEmbedded && auth.AccessToken != "" && float64(auth.Expiry)-agyTokenSkewSec > now {
		return auth.AccessToken, nil
	}

	if clientID == "" || clientSecret == "" {
		return "", ErrPerm
	}

	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {auth.RefreshToken},
		"grant_type":    {"refresh_token"},
	}.Encode()
	tokenURL := auth.TokenURI
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

func (g *Antigravity) endpoints(env *Env, auth *agySlotAuth) []string {
	if auth != nil && (strings.EqualFold(auth.AuthMethod, "oauth-business") || strings.EqualFold(auth.AuthMethod, "gcp")) {
		return []string{agyProdEndpoint, agyDailyEndpoint}
	}
	if t3Paths, defaultDir, inst := g.t3TokenPaths(env); inst {
		dir := defaultDir
		if dir == "" && len(t3Paths) > 0 {
			dir = filepath.Dir(t3Paths[0])
		}
		if dir != "" {
			settings := store.ReadJSONMap(filepath.Join(dir, "settings.json"))
			if getString(getMap(settings, "auth"), "type") == "oauth-business" {
				return []string{agyProdEndpoint, agyDailyEndpoint}
			}
		}
	}
	return []string{agyDailyEndpoint, agyProdEndpoint}
}

func (g *Antigravity) resolveProjectID(env *Env, auth *agySlotAuth) string {
	if auth != nil && auth.ProjectID != "" {
		return auth.ProjectID
	}
	for _, k := range []string{"TORI_AGY_PROJECT_ID", "ANTIGRAVITY_PROJECT_ID", "GEMINI_DEFAULT_PROJECT"} {
		if v := strings.TrimSpace(env.getenv(k)); v != "" {
			return v
		}
	}
	if env.Home != "" {
		p := filepath.Join(env.Home, ".gemini", "antigravity-cli", "cache", "default_project_id.txt")
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return ""
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

func (g *Antigravity) queryQuota(env *Env, auth *agySlotAuth, at, project string) ([]Window, error) {
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
	endpoints := g.endpoints(env, auth)
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
	auth := g.readSlotAuth(slot.Dir)
	if auth == nil || auth.RefreshToken == "" {
		return nil, errors.New("no token")
	}
	at, err := g.accessToken(env, slot.Dir, auth, false)
	if err != nil {
		return nil, err
	}
	g.learnEmail(env, slot.Dir, slot.Meta, at)
	project := g.resolveProjectID(env, auth)
	wins, err := g.queryQuota(env, auth, at, project)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) {
			g.dropAccessToken(slot.Dir)
			at2, err2 := g.accessToken(env, slot.Dir, auth, true)
			if err2 != nil {
				return nil, err2
			}
			return g.queryQuota(env, auth, at2, project)
		}
		return nil, err
	}
	return wins, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileModTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
