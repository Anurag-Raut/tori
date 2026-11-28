package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// t3Runtime is a resolved antigravity-acp program and how to start it.
type t3Runtime struct {
	Path    string
	Harness string // localharness_external, or "" for the agy CLI
	Args    []string
}

var errT3NoRuntime = errors.New("Install Antigravity in T3 Code's settings first.")

func isAgyName(path string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		base = strings.TrimSuffix(base, ext)
	}
	return base == "agy" || base == "antigravity"
}

func t3Platform(env *Env) (string, string) {
	osName := env.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	arch := env.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	}
	return osName, arch
}

func t3ExeNames(env *Env) (server, harness string) {
	if env.GOOS == "windows" {
		return "agy_acp_server.exe", "localharness_external.exe"
	}
	return "agy_acp_server.par", "localharness_external"
}

func (rt *t3Runtime) withArgs(env *Env) *t3Runtime {
	if rt.Harness != "" && env.GOOS == "linux" {
		rt.Args = []string{"--uid="}
	}
	return rt
}

// t3ResolveRuntime mirrors T3 Code's lookup: a custom path, the release T3 Code
// installed, then the server on PATH. It never downloads anything.
func t3ResolveRuntime(env *Env, in t3Instance) (*t3Runtime, error) {
	server, harness := t3ExeNames(env)
	if p := in.BinaryPath; p != "" {
		resolved := p
		if !strings.ContainsAny(p, `/\`) && env.LookPath != nil {
			if lp, err := env.LookPath(p); err == nil {
				resolved = lp
			}
		}
		if !fileExists(resolved) {
			return nil, errors.New("T3 Code's custom Antigravity path doesn't exist. Fix it in T3 Code's settings.")
		}
		if isAgyName(resolved) {
			return &t3Runtime{Path: resolved, Args: []string{"acp"}}, nil
		}
		h := filepath.Join(filepath.Dir(resolved), harness)
		if !fileExists(h) {
			return nil, errors.New("T3 Code's custom Antigravity path has no localharness_external next to it.")
		}
		return (&t3Runtime{Path: resolved, Harness: h}).withArgs(env), nil
	}
	osName, arch := t3Platform(env)
	if base := t3BaseDir(env); base != "" {
		dir := filepath.Join(base, "tools", "antigravity-acp", osName+"-"+arch)
		if raw, err := os.ReadFile(filepath.Join(dir, "active.json")); err == nil {
			var active struct {
				ReleaseID string `json:"releaseId"`
			}
			id := ""
			if json.Unmarshal(raw, &active) == nil {
				id = active.ReleaseID
			}
			if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
				return nil, errT3NoRuntime
			}
			v := filepath.Join(dir, "versions", id)
			if fileExists(filepath.Join(v, server)) && fileExists(filepath.Join(v, harness)) {
				return (&t3Runtime{Path: filepath.Join(v, server), Harness: filepath.Join(v, harness)}).withArgs(env), nil
			}
			return nil, errT3NoRuntime
		}
	}
	if env.LookPath != nil {
		if p, err := env.LookPath(server); err == nil {
			h := filepath.Join(filepath.Dir(p), harness)
			if fileExists(h) {
				return (&t3Runtime{Path: p, Harness: h}).withArgs(env), nil
			}
		}
	}
	return nil, errT3NoRuntime
}

var t3RemovedEnv = map[string]bool{
	"GEMINI_API_KEY": true, "GOOGLE_API_KEY": true, "GOOGLE_APPLICATION_CREDENTIALS": true,
	"GOOGLE_CLOUD_PROJECT": true, "GOOGLE_CLOUD_LOCATION": true, "GOOGLE_CLOUD_QUOTA_PROJECT": true,
	"GOOGLE_GENAI_USE_VERTEXAI": true, "GCLOUD_PROJECT": true, "CLOUDSDK_CORE_PROJECT": true,
	"AGY_ACP_CCPA_PROJECT": true, "AGY_ACP_ENABLE_OAUTH": true, "GEMINI_HOME": true,
	"AGY_ACP_FORCE_FILE_STORAGE": true, "ANTIGRAVITY_HARNESS_PATH": true, "BROWSER": true,
	"PYTHONUNBUFFERED": true, "ELECTRON_RUN_AS_NODE": true, "CHROME_CRASHPAD_PIPE_NAME": true,
}

// t3SpawnEnv builds the runtime's environment like T3 Code does. base is a list
// of KEY=VALUE strings.
func t3SpawnEnv(env *Env, base []string, rt *t3Runtime, profile, acpTmp, browser string) []string {
	var out []string
	has := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if t3RemovedEnv[strings.ToUpper(k)] {
			continue
		}
		has[strings.ToUpper(k)] = true
		out = append(out, kv)
	}
	set := func(k, v string) {
		out = append(out, k+"="+v)
	}
	if env.GOOS == "linux" && !has["SSL_CERT_FILE"] && !has["SSL_CERT_DIR"] {
		cands := []string{}
		if v := env.getenv("NIX_SSL_CERT_FILE"); v != "" {
			cands = append(cands, v)
		}
		cands = append(cands, "/etc/ssl/certs/ca-bundle.crt", "/etc/ssl/certs/ca-certificates.crt",
			"/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem")
		for _, c := range cands {
			if fileExists(c) {
				set("SSL_CERT_FILE", c)
				break
			}
		}
	}
	set("GEMINI_HOME", profile)
	set("AGY_ACP_FORCE_FILE_STORAGE", "1")
	set("PYTHONUNBUFFERED", "1")
	if browser != "" {
		set("BROWSER", browser)
	}
	if rt.Harness != "" {
		set("ANTIGRAVITY_HARNESS_PATH", rt.Harness)
	}
	if env.GOOS == "windows" {
		set("TEMP", acpTmp)
		set("TMP", acpTmp)
	} else {
		set("TMPDIR", acpTmp)
	}
	return out
}

// t3BrowserCommand is the BROWSER value: Tori opens the link itself, so exactly
// one tab opens. %s is the link.
func t3BrowserCommand(exe string, windows bool) string {
	if windows {
		exe = strings.ReplaceAll(exe, "\\", "/")
	}
	return "'" + strings.ReplaceAll(exe, "'", `'"'"'`) + "' __open-url %s"
}

// t3PrepareProfile creates T3 Code's private folders and, only when missing,
// the small settings file that names the sign-in method. It holds no secret.
func t3PrepareProfile(env *Env, in t3Instance) (profile, acp, tmp string, err error) {
	profile = t3ProfileDir(env, in.ID)
	if profile == "" {
		return "", "", "", errors.New("can't find T3 Code's data folder")
	}
	acp = filepath.Join(profile, "antigravity-acp")
	tmp = filepath.Join(acp, "tmp")
	if err = os.MkdirAll(tmp, 0o700); err != nil {
		return
	}
	settings := filepath.Join(acp, "settings.json")
	if _, serr := os.Stat(settings); errors.Is(serr, os.ErrNotExist) {
		f, oerr := os.OpenFile(settings, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if oerr == nil {
			_, err = f.WriteString(t3ProfileConfig)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		} else if !errors.Is(oerr, os.ErrExist) {
			err = oerr
		}
	}
	return
}

// t3SignedIn reports whether the runtime wrote its login. It only stats the file.
func t3SignedIn(profile string) bool {
	_, err := os.Stat(filepath.Join(profile, "antigravity-acp", "acp_token.json"))
	return err == nil
}
