//go:build windows

package provider

import (
	"fmt"
	"syscall"

	"github.com/danieljoos/wincred"
	"github.com/zalando/go-keyring"
)

// osKeyring uses Windows Credential Manager directly so Tori reads and writes both
// Go go-keyring ("<service>:<user>") and Rust keyring ("<user>.<service>") entries.
type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	var lastErr error
	for _, target := range windowsCredTargetNames(service, user) {
		cred, err := wincred.GetGenericCredential(target)
		if err == nil {
			return decodeWindowsCredBlob(cred.CredentialBlob), nil
		}
		if err == syscall.ERROR_NOT_FOUND {
			continue
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", keyring.ErrNotFound
}

func (osKeyring) Set(service, user, secret string) error {
	targets := windowsCredTargetNames(service, user)
	chosen := targets[0]
	var existing *wincred.GenericCredential
	for _, target := range targets {
		if cred, err := wincred.GetGenericCredential(target); err == nil {
			chosen = target
			existing = cred
			break
		}
	}
	var existingBlob []byte
	if existing != nil {
		existingBlob = existing.CredentialBlob
	}
	blob := encodeWindowsCredBlob(chosen, service, user, secret, existingBlob)
	if len(blob) > windowsCredMaxBlobBytes {
		return fmt.Errorf("encoded credential (%d bytes) exceeds Windows Credential Manager %d-byte limit: %w", len(blob), windowsCredMaxBlobBytes, keyring.ErrSetDataTooBig)
	}
	cred := wincred.NewGenericCredential(chosen)
	cred.UserName = user
	cred.CredentialBlob = blob
	if existing != nil && existing.Persist != 0 {
		cred.Persist = existing.Persist
	}
	return cred.Write()
}

func (osKeyring) Delete(service, user string) error {
	var lastErr error
	for _, target := range windowsCredTargetNames(service, user) {
		cred, err := wincred.GetGenericCredential(target)
		if err != nil {
			if err == syscall.ERROR_NOT_FOUND {
				continue
			}
			lastErr = err
			continue
		}
		if err := cred.Delete(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
