//go:build darwin

package provider

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
)

const darwinSecurityBin = "/usr/bin/security"

// osKeyring uses macOS `/usr/bin/security` directly so secrets are stored as raw
// UTF-8/JSON bytes (compatible with Claude Code, Codex, and Antigravity CLI) without
// go-keyring's base64 wrapper or CGO Security.framework ACL mismatches.
type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	out, err := exec.Command(
		darwinSecurityBin,
		"find-generic-password",
		"-s", service,
		"-a", user,
		"-w",
	).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "could not be found") {
			return "", keyring.ErrNotFound
		}
		return "", fmt.Errorf("keychain read failed: %w", err)
	}
	return decodeKeychainSecret(string(out)), nil
}

func (osKeyring) Set(service, user, secret string) error {
	cmdLine := buildSecurityAddCommand(service, user, secret)
	if !strings.ContainsAny(service+user, "\r\n") && len(cmdLine) <= darwinSecurityInteractiveMaxLen {
		cmd := exec.Command(darwinSecurityBin, "-i")
		cmd.Stdin = strings.NewReader(cmdLine)
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if err != nil {
			return fmt.Errorf("keychain write failed: %w (%s)", err, msg)
		}
		if strings.Contains(strings.ToLower(msg), "security:") {
			return fmt.Errorf("keychain write failed: %s", msg)
		}
		return nil
	}
	// Payloads whose hex-encoded command exceeds `security -i`'s 4,095-byte stdin line
	// buffer fall back to direct argv invocation (matching Claude Code's behavior).
	hexSecret := hex.EncodeToString([]byte(secret))
	out, err := exec.Command(
		darwinSecurityBin,
		"add-generic-password",
		"-U",
		"-a", user,
		"-s", service,
		"-X", hexSecret,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain write failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (osKeyring) Delete(service, user string) error {
	out, err := exec.Command(
		darwinSecurityBin,
		"delete-generic-password",
		"-s", service,
		"-a", user,
	).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete failed: %w", err)
	}
	return nil
}
