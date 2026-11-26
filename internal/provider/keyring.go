package provider

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

const (
	goKeyringHexPrefix    = "go-keyring-encoded:"
	goKeyringBase64Prefix = "go-keyring-base64:"
	// macOS `security -i` has a 4,095-byte stdin line limit; stay safely below it.
	darwinSecurityInteractiveMaxLen = 4000
	// Windows Credential Manager generic credential blob limit.
	windowsCredMaxBlobBytes = 2560
)

func isKeyringNotFound(err error) bool {
	return errors.Is(err, keyring.ErrNotFound)
}

// decodeKeychainSecret normalizes raw output from macOS `/usr/bin/security find-generic-password -w`.
// It handles legacy go-keyring prefixes as well as macOS's automatic hex encoding of payloads
// containing newlines or non-ASCII bytes.
func decodeKeychainSecret(raw string) string {
	trimmed := strings.TrimRight(raw, "\r\n")
	if strings.HasPrefix(trimmed, goKeyringHexPrefix) {
		if dec, err := hex.DecodeString(trimmed[len(goKeyringHexPrefix):]); err == nil {
			return string(dec)
		}
	}
	if strings.HasPrefix(trimmed, goKeyringBase64Prefix) {
		if dec, err := base64.StdEncoding.DecodeString(trimmed[len(goKeyringBase64Prefix):]); err == nil {
			return string(dec)
		}
	}
	if len(trimmed) >= 2 && len(trimmed)%2 == 0 && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") && isHexString(trimmed) {
		if dec, err := hex.DecodeString(trimmed); err == nil && utf8.Valid(dec) {
			s := strings.TrimSpace(string(dec))
			if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				return string(dec)
			}
		}
	}
	return trimmed
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

func quoteSecurityArg(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// buildSecurityAddCommand builds a `security -i` command storing raw secret bytes via `-X <hex>`.
func buildSecurityAddCommand(service, user, secret string) string {
	return fmt.Sprintf(
		"add-generic-password -U -a %s -s %s -X %s\n",
		quoteSecurityArg(user),
		quoteSecurityArg(service),
		hex.EncodeToString([]byte(secret)),
	)
}

// windowsCredTargetNames returns Windows Credential Manager target names in preferred order.
// Rust's keyring crate (used by Codex) stores "<user>.<service>", while Go's go-keyring
// (used by Go CLI tools) stores "<service>:<user>".
func windowsCredTargetNames(service, user string) []string {
	rustStyle := user + "." + service
	goStyle := service + ":" + user
	if service == codexKeyringService {
		return []string{rustStyle, goStyle}
	}
	return []string{goStyle, rustStyle}
}

func isRustStyleWindowsTarget(target, service, user string) bool {
	return target == user+"."+service
}

// isWindowsUTF16LEBlob detects UTF-16LE encoded ASCII/JSON Credential Manager blobs
// (as written by Rust's `keyring` crate via `str::encode_utf16`).
func isWindowsUTF16LEBlob(blob []byte) bool {
	if len(blob) < 2 || len(blob)%2 != 0 || blob[0] == 0 {
		return false
	}
	nulOdd := 0
	check := len(blob)
	if check > 32 {
		check = 32
	}
	for i := 1; i < check; i += 2 {
		if blob[i] == 0 {
			nulOdd++
		}
	}
	return nulOdd > 0 && nulOdd >= (check/2)-2
}

// encodeWindowsUTF16LE encodes s as UTF-16LE bytes without a trailing NUL,
// matching Rust's `keyring` crate on Windows (`str::encode_utf16`).
func encodeWindowsUTF16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, len(units)*2)
	for i, u := range units {
		out[2*i] = byte(u)
		out[2*i+1] = byte(u >> 8)
	}
	return out
}

// encodeWindowsCredBlob encodes secret for Windows Credential Manager:
// Rust-style targets ("<user>.<service>") and existing UTF-16LE entries are encoded as UTF-16LE
// so Rust's `keyring` crate (`String::from_utf16`) reads them cleanly; Go-style targets use UTF-8.
func encodeWindowsCredBlob(target, service, user, secret string, existingBlob []byte) []byte {
	if isRustStyleWindowsTarget(target, service, user) || isWindowsUTF16LEBlob(existingBlob) {
		return encodeWindowsUTF16LE(secret)
	}
	return []byte(secret)
}

// decodeWindowsCredBlob decodes raw Windows Credential Manager bytes, handling both
// plain UTF-8 (go-keyring / wincred) and UTF-16LE (Rust keyring / native Windows entries).
func decodeWindowsCredBlob(blob []byte) string {
	if isWindowsUTF16LEBlob(blob) {
		u16 := make([]uint16, len(blob)/2)
		for i := range u16 {
			u16[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
		}
		// Trim trailing NUL if present.
		for len(u16) > 0 && u16[len(u16)-1] == 0 {
			u16 = u16[:len(u16)-1]
		}
		return string(utf16.Decode(u16))
	}
	return string(blob)
}
