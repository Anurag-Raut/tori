package provider

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

const (
	agyKeyringService = "gemini"
	agyKeyringUser    = "antigravity"

	agyKeyUnifiedOAuth = "antigravityUnifiedStateSync.oauthToken"
	agyKeyAuthStatus   = "antigravityAuthStatus"
	agyKeyOnboarding   = "antigravityOnboarding"
	agyKeyLegacyAuth   = "google.antigravity"
)

// SHA-256 fingerprints of the public Antigravity OAuth client embedded in installed
// Antigravity binaries (agy CLI and language_server). No secrets are stored in this repo.
const (
	agyClientIDHash        = "bf00c418024ba6bf606ccdc37120976e41bc429dd1d46ecf16a729aa532626ea"
	agyClientSecretHashV1  = "1d2f041093fd95aa8995a038c711d50a7960da09a505381c09a745d6ad0ecc60"
	agyClientSecretHashV2  = "23d89fc5fbce73ed5b9e0cf0ca786bd0eb1bcdff8f65d03dc022d299ad2234b6"
	agyGCPClientIDHash     = "69dee430c4e956f6b672b99a846566d6a86814999fc3b3b050697c0d65ea80c3"
	agyGCPClientSecretHash = "a2ddedc84850a9c48b7e01d61700848083bf0e10fb8d6c99c496803e8b48ced0"

	agyClientIDSuffix        = ".apps.googleusercontent.com"
	agyClientSecretPrefix    = "GOCSPX-"
	agyDiscoveryCarry        = 128
	agyMinClientIDLength     = 40
	agyMinClientSecretLength = 20
)

func defaultLookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// agyStateDBSnap stores the raw ItemTable auth values captured from state.vscdb.
type agyStateDBSnap struct {
	OAuthToken string `json:"oauthToken,omitempty"`
	AuthStatus string `json:"authStatus,omitempty"`
}

func openStateDB(dbPath string, readOnly bool) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(`PRAGMA busy_timeout = 2000`); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if readOnly {
		_, _ = conn.Exec(`PRAGMA query_only = ON`)
	}
	return conn, nil
}

// readStateDBAuth reads the Antigravity OAuth token and auth status from state.vscdb.
// Returns (nil, ..., nil) when the DB exists but has no Antigravity login.
func readStateDBAuth(dbPath string) (snap *agyStateDBSnap, access, refresh string, expiry int64, email, name string, err error) {
	conn, err := openStateDB(dbPath, true)
	if err != nil {
		return nil, "", "", 0, "", "", err
	}
	defer conn.Close()

	var oauthB64 string
	err = conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, agyKeyUnifiedOAuth).Scan(&oauthB64)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, "", "", 0, "", "", nil
		}
		return nil, "", "", 0, "", "", err
	}
	oauthB64 = strings.TrimSpace(oauthB64)
	if oauthB64 == "" {
		return nil, "", "", 0, "", "", nil
	}

	access, refresh, expiry, err = extractOAuthFromUnified(oauthB64)
	if err != nil || refresh == "" {
		return nil, "", "", 0, "", "", err
	}

	var authStatus string
	_ = conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, agyKeyAuthStatus).Scan(&authStatus)
	if authStatus != "" {
		var parsed struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if json.Unmarshal([]byte(authStatus), &parsed) == nil {
			email = strings.TrimSpace(parsed.Email)
			name = strings.TrimSpace(parsed.Name)
		}
	}

	return &agyStateDBSnap{
		OAuthToken: oauthB64,
		AuthStatus: authStatus,
	}, access, refresh, expiry, email, name, nil
}

// writeStateDBAuth atomically updates Antigravity auth entries inside state.vscdb.
func writeStateDBAuth(dbPath string, snap *agyStateDBSnap) error {
	if snap == nil || snap.OAuthToken == "" {
		return errors.New("missing Antigravity state.vscdb snapshot")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	conn, err := openStateDB(dbPath, false)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	upsert := func(k, v string) error {
		_, err := tx.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v)
		return err
	}
	if err := upsert(agyKeyUnifiedOAuth, snap.OAuthToken); err != nil {
		_ = tx.Rollback()
		return err
	}
	if snap.AuthStatus != "" {
		if err := upsert(agyKeyAuthStatus, snap.AuthStatus); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := upsert(agyKeyOnboarding, "true"); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ItemTable WHERE key = ?`, agyKeyLegacyAuth); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// clearStateDBAuth removes Antigravity auth keys from state.vscdb without disturbing
// other IDE settings, returning a restore function.
func clearStateDBAuth(dbPath string) (func() error, error) {
	conn, err := openStateDB(dbPath, false)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	keys := []string{agyKeyUnifiedOAuth, agyKeyAuthStatus, agyKeyLegacyAuth}
	saved := make(map[string]string, len(keys))
	for _, k := range keys {
		var v string
		err := conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, k).Scan(&v)
		if err == nil {
			saved[k] = v
		} else if !errors.Is(err, sql.ErrNoRows) {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				return nil, nil
			}
			return nil, err
		}
	}
	if len(saved) == 0 {
		return nil, nil
	}

	tx, err := conn.Begin()
	if err != nil {
		return nil, err
	}
	for k := range saved {
		if _, err := tx.Exec(`DELETE FROM ItemTable WHERE key = ?`, k); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	restore := func() error {
		c, err := openStateDB(dbPath, false)
		if err != nil {
			return err
		}
		defer c.Close()
		rtx, err := c.Begin()
		if err != nil {
			return err
		}
		for k, v := range saved {
			if _, err := rtx.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				_ = rtx.Rollback()
				return err
			}
		}
		return rtx.Commit()
	}
	return restore, nil
}

// isAntigravityProcessRunning reports whether the Antigravity Desktop or IDE GUI app is running.
func isAntigravityProcessRunning(surface string) bool {
	selfPID := os.Getpid()
	switch runtime.GOOS {
	case "linux":
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return false
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(ent.Name())
			if err != nil || pid <= 0 || pid == selfPID {
				continue
			}
			raw, err := os.ReadFile(filepath.Join("/proc", ent.Name(), "cmdline"))
			if err != nil || len(raw) == 0 {
				continue
			}
			cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
			if matchAntigravityProcess(cmdline, surface) {
				return true
			}
		}
		return false
	case "darwin":
		out, err := exec.Command("ps", "-axo", "pid=,args=").Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pid, err := strconv.Atoi(fields[0])
			if err == nil && pid == selfPID {
				continue
			}
			cmdline := strings.Join(fields[1:], " ")
			if matchAntigravityProcess(cmdline, surface) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func matchAntigravityProcess(cmdline, surface string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmdline))
	if lower == "" || !strings.Contains(lower, "antigravity") {
		return false
	}
	// Exclude non-GUI processes: T3 Code ACP harness, agy CLI, account managers, and tori itself.
	for _, excl := range []string{
		"antigravity-acp",
		"agy_acp_server",
		"localharness",
		"antigravity-manager",
		"antigravity_manager",
		"antigravity manager",
		"agy-auth",
		"antigravity-cli",
	} {
		if strings.Contains(lower, excl) {
			return false
		}
	}
	fields := strings.Fields(lower)
	if len(fields) > 0 {
		base := filepath.Base(fields[0])
		if base == "tori" || base == "agy" || base == "grep" || base == "rg" || base == "ps" {
			return false
		}
	}
	isIDE := strings.Contains(lower, "antigravity ide") ||
		strings.Contains(lower, "antigravityide") ||
		strings.Contains(lower, "antigravity-ide")
	switch surface {
	case "ide":
		return isIDE
	case "desktop":
		if isIDE {
			return false
		}
		// Require a path/app reference to Antigravity rather than an unrelated argument.
		return strings.Contains(lower, "/antigravity") ||
			strings.Contains(lower, `\antigravity`) ||
			strings.Contains(lower, "antigravity.app") ||
			strings.Contains(lower, "antigravity.exe") ||
			(len(fields) > 0 && filepath.Base(fields[0]) == "antigravity")
	default:
		return false
	}
}

type agyClientCreds struct {
	clientID     string
	clientSecret string
	gcpID        string
	gcpSecret    string
}

var (
	agyClientCacheMu sync.Mutex
	agyClientCache   = map[string]agyClientCreds{}
)

// discoverAgyOAuthClient resolves the public Antigravity OAuth client ID and secret
// from env overrides or installed Antigravity binaries (matching SHA-256 hashes only).
func discoverAgyOAuthClient(env *Env, isGCP bool) (string, string, bool) {
	if isGCP {
		if id, sec := env.getenv("TORI_AGY_GCP_CLIENT_ID"), env.getenv("TORI_AGY_GCP_CLIENT_SECRET"); id != "" && sec != "" {
			return id, sec, true
		}
		if id, sec := env.getenv("AGY_AUTH_GCP_CLIENT_ID"), env.getenv("AGY_AUTH_GCP_CLIENT_SECRET"); id != "" && sec != "" {
			return id, sec, true
		}
	} else {
		if id, sec := env.getenv("TORI_AGY_CLIENT_ID"), env.getenv("TORI_AGY_CLIENT_SECRET"); id != "" && sec != "" {
			return id, sec, true
		}
		if id, sec := env.getenv("AGY_AUTH_CLIENT_ID"), env.getenv("AGY_AUTH_CLIENT_SECRET"); id != "" && sec != "" {
			return id, sec, true
		}
	}

	candidates := agyBinaryCandidates(env)
	for _, cand := range candidates {
		resolved := resolveAgyWrapper(cand)
		if resolved == "" {
			continue
		}
		creds, ok := scanAgyBinaryCached(resolved)
		if !ok {
			continue
		}
		if isGCP && creds.gcpID != "" && creds.gcpSecret != "" {
			return creds.gcpID, creds.gcpSecret, true
		}
		if !isGCP && creds.clientID != "" && creds.clientSecret != "" {
			return creds.clientID, creds.clientSecret, true
		}
	}
	return "", "", false
}

func agyBinaryCandidates(env *Env) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	addGlob := func(pattern string) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return
		}
		for _, m := range matches {
			add(m)
		}
	}

	add(env.getenv("TORI_AGY_BIN"))
	add(env.getenv("AGY_AUTH_AGY_PATH"))

	if env.LookPath != nil {
		if p, err := env.LookPath("agy"); err == nil {
			add(p)
		}
	}
	if env.Home != "" {
		add(filepath.Join(env.Home, ".local", "bin", "agy"))
		add(filepath.Join(env.Home, ".gemini", "antigravity-cli", "bin", "agy"))
	}
	// Only scan system install paths when LookPath is configured (avoids leaking host binaries into isolated unit tests).
	if env.LookPath != nil {
		switch env.GOOS {
		case "darwin":
			add("/opt/homebrew/bin/agy")
			add("/usr/local/bin/agy")
			addGlob("/Applications/Antigravity.app/Contents/Resources/app/extensions/antigravity/bin/language_server_*")
			addGlob("/Applications/Antigravity IDE.app/Contents/Resources/app/extensions/antigravity/bin/language_server_*")
			if env.Home != "" {
				addGlob(filepath.Join(env.Home, "Applications", "Antigravity.app", "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_*"))
				addGlob(filepath.Join(env.Home, "Applications", "Antigravity IDE.app", "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_*"))
			}
		case "linux":
			add("/usr/local/bin/agy")
			add("/usr/bin/agy")
			addGlob("/usr/lib/antigravity/extensions/antigravity/bin/language_server_*")
			addGlob("/usr/share/antigravity/resources/app/extensions/antigravity/bin/language_server_*")
			addGlob("/opt/Antigravity/resources/app/extensions/antigravity/bin/language_server_*")
			addGlob("/usr/lib/antigravity-ide/extensions/antigravity/bin/language_server_*")
			addGlob("/usr/share/antigravity-ide/resources/app/extensions/antigravity/bin/language_server_*")
		}
	}
	return out
}

func scanAgyBinaryCached(path string) (agyClientCreds, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return agyClientCreds{}, false
	}
	cacheKey := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	agyClientCacheMu.Lock()
	if cached, ok := agyClientCache[cacheKey]; ok {
		agyClientCacheMu.Unlock()
		return cached, cached.clientID != "" || cached.gcpID != ""
	}
	agyClientCacheMu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return agyClientCreds{}, false
	}
	defer f.Close()

	creds, err := scanAgyClientsFromReader(f)
	if err != nil {
		return agyClientCreds{}, false
	}
	agyClientCacheMu.Lock()
	agyClientCache[cacheKey] = creds
	agyClientCacheMu.Unlock()
	return creds, creds.clientID != "" || creds.gcpID != ""
}

func scanAgyClientsFromReader(r io.Reader) (agyClientCreds, error) {
	ids := make(map[string]bool)
	secrets := make(map[string]bool)
	buf := make([]byte, 1<<20)
	carry := make([]byte, 0, agyDiscoveryCarry)

	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, 0, len(carry)+n)
			chunk = append(chunk, carry...)
			chunk = append(chunk, buf[:n]...)
			collectAgyCandidates(chunk, ids, secrets)
			if len(chunk) > agyDiscoveryCarry {
				carry = append(carry[:0], chunk[len(chunk)-agyDiscoveryCarry:]...)
			} else {
				carry = append(carry[:0], chunk...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return agyClientCreds{}, readErr
		}
	}

	cid, csec := matchAgyClientPair(ids, secrets, agyClientIDHash, agyClientSecretHashV1, agyClientSecretHashV2)
	gid, gsec := matchAgyClientPair(ids, secrets, agyGCPClientIDHash, agyGCPClientSecretHash)
	return agyClientCreds{
		clientID:     cid,
		clientSecret: csec,
		gcpID:        gid,
		gcpSecret:    gsec,
	}, nil
}

func matchAgyClientPair(ids, secrets map[string]bool, wantIDHash string, wantSecHashes ...string) (string, string) {
	var id, sec string
	for cand := range ids {
		if sha256Hex(cand) == wantIDHash {
			id = cand
			break
		}
	}
	if id == "" {
		return "", ""
	}
	for cand := range secrets {
		h := sha256Hex(cand)
		for _, want := range wantSecHashes {
			if h == want {
				sec = cand
				break
			}
		}
		if sec != "" {
			break
		}
	}
	if sec == "" {
		return "", ""
	}
	return id, sec
}

func collectAgyCandidates(chunk []byte, ids, secrets map[string]bool) {
	prefix := []byte(agyClientSecretPrefix)
	for offset := 0; ; {
		idx := bytes.Index(chunk[offset:], prefix)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(prefix)
		for end < len(chunk) && isAgySecretChar(chunk[end]) {
			end++
		}
		run := chunk[start:end]
		if len(run) > agyDiscoveryCarry {
			run = run[:agyDiscoveryCarry]
		}
		for l := agyMinClientSecretLength; l <= len(run); l++ {
			secrets[string(run[:l])] = true
		}
		offset = end
	}

	suffix := []byte(agyClientIDSuffix)
	for offset := 0; ; {
		idx := bytes.Index(chunk[offset:], suffix)
		if idx < 0 {
			break
		}
		suffixStart := offset + idx
		start := suffixStart
		for start > 0 && isAgyClientIDChar(chunk[start-1]) {
			start--
		}
		run := chunk[start:suffixStart]
		if len(run) > agyDiscoveryCarry {
			run = run[len(run)-agyDiscoveryCarry:]
		}
		end := suffixStart + len(suffix)
		for l := agyMinClientIDLength; l <= len(run); l++ {
			ids[string(run[len(run)-l:])+agyClientIDSuffix] = true
		}
		offset = end
	}
}

func isAgySecretChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
}

func isAgyClientIDChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// resolveAgyWrapper follows symlinks and simple `#!` shell wrapper scripts that `exec`
// a real binary (e.g. Homebrew cask wrappers) up to 3 hops.
func resolveAgyWrapper(path string) string {
	cur := strings.TrimSpace(path)
	if cur == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(cur); err == nil {
		cur = real
	}
	seen := map[string]bool{}
	for hop := 0; hop < 3; hop++ {
		cleaned := filepath.Clean(cur)
		if seen[cleaned] {
			break
		}
		seen[cleaned] = true
		f, err := os.Open(cur)
		if err != nil {
			break
		}
		buf := make([]byte, 4096)
		n, readErr := f.Read(buf)
		_ = f.Close()
		if n < 2 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
			break
		}
		head := bytes.TrimPrefix(buf[:n], []byte("\xef\xbb\xbf"))
		if !bytes.HasPrefix(head, []byte("#!")) {
			break
		}
		target := parseAgyExecTarget(string(head), filepath.Dir(cur))
		if target == "" {
			break
		}
		st, err := os.Stat(target)
		if err != nil || st.IsDir() {
			break
		}
		cur = target
	}
	return cur
}

func parseAgyExecTarget(script, baseDir string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		for i := 0; i < len(fields); i++ {
			if fields[i] != "exec" {
				continue
			}
			i++
			for i < len(fields) && strings.HasPrefix(fields[i], "-") {
				if fields[i] == "-a" {
					i += 2
					continue
				}
				if fields[i] == "--" {
					i++
					break
				}
				i++
			}
			if i >= len(fields) {
				break
			}
			cand := strings.Trim(fields[i], `"'`)
			if cand == "" || strings.Contains(cand, "$") {
				break
			}
			if !filepath.IsAbs(cand) && baseDir != "" {
				cand = filepath.Join(baseDir, cand)
			}
			return cand
		}
	}
	return ""
}
