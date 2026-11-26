//go:build windows

package provider

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/danieljoos/wincred"
	"github.com/zalando/go-keyring"
)

func TestWindowsCredentialManagerRoundTrip(t *testing.T) {
	kr := osKeyring{}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// 1. Codex Rust-style target ("<user>.<service>") must be stored as UTF-16LE
	//    so Codex's Rust `keyring` crate (`String::from_utf16`) reads it back cleanly.
	codexUser := "cli|tori-test-" + suffix
	codexTarget := codexUser + "." + codexKeyringService
	codexSecret := `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-tori-test-wincred-utf16"}`
	t.Cleanup(func() {
		_ = kr.Delete(codexKeyringService, codexUser)
	})

	if err := kr.Set(codexKeyringService, codexUser, codexSecret); err != nil {
		t.Fatalf("osKeyring.Set(codex) failed: %v", err)
	}
	rawCred, err := wincred.GetGenericCredential(codexTarget)
	if err != nil {
		t.Fatalf("expected Rust-style credential %q in Credential Manager: %v", codexTarget, err)
	}
	wantUTF16 := encodeWindowsUTF16LE(codexSecret)
	if !bytes.Equal(rawCred.CredentialBlob, wantUTF16) {
		t.Fatalf("expected UTF-16LE blob (%d bytes) for Rust-style target, got %d bytes", len(wantUTF16), len(rawCred.CredentialBlob))
	}
	got, err := kr.Get(codexKeyringService, codexUser)
	if err != nil || got != codexSecret {
		t.Fatalf("osKeyring.Get(codex) = (%q, %v), want (%q, nil)", got, err, codexSecret)
	}

	// Oversized UTF-16LE payload (1400 ASCII bytes -> 2800 UTF-16LE bytes > 2560)
	// must fail cleanly with ErrSetDataTooBig and leave the existing entry intact.
	tooBig := strings.Repeat("x", 1400)
	if err := kr.Set(codexKeyringService, codexUser, tooBig); !errors.Is(err, keyring.ErrSetDataTooBig) {
		t.Fatalf("expected ErrSetDataTooBig for 2800-byte UTF-16LE blob, got %v", err)
	}
	if got, err := kr.Get(codexKeyringService, codexUser); err != nil || got != codexSecret {
		t.Fatalf("existing credential corrupted after ErrSetDataTooBig: got (%q, %v)", got, err)
	}

	if err := kr.Delete(codexKeyringService, codexUser); err != nil {
		t.Fatalf("osKeyring.Delete(codex) failed: %v", err)
	}
	if _, err := kr.Get(codexKeyringService, codexUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// 2. Go-style target ("<service>:<user>") must default to UTF-8.
	goService := "tori-test-service-" + suffix
	goUser := "antigravity"
	goTarget := goService + ":" + goUser
	goSecret := `{"token":{"refresh_token":"1//0rt-wincred-utf8"}}`
	t.Cleanup(func() {
		_ = kr.Delete(goService, goUser)
	})

	if err := kr.Set(goService, goUser, goSecret); err != nil {
		t.Fatalf("osKeyring.Set(go-style) failed: %v", err)
	}
	rawGoCred, err := wincred.GetGenericCredential(goTarget)
	if err != nil {
		t.Fatalf("expected Go-style credential %q in Credential Manager: %v", goTarget, err)
	}
	if string(rawGoCred.CredentialBlob) != goSecret {
		t.Fatalf("expected UTF-8 blob %q for Go-style target, got %q", goSecret, string(rawGoCred.CredentialBlob))
	}
	if got, err := kr.Get(goService, goUser); err != nil || got != goSecret {
		t.Fatalf("osKeyring.Get(go-style) = (%q, %v), want (%q, nil)", got, err, goSecret)
	}
}
