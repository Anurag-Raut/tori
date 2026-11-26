package provider

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

const (
	goKeyringHexPrefix    = "go-keyring-encoded:"
	goKeyringBase64Prefix = "go-keyring-base64:"
	// macOS `security -i` has a 4,095-byte stdin line limit; stay safely below it.
	darwinSecurityInteractiveMaxLen = 4000
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
