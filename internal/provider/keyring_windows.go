//go:build windows

package provider

import (
	"fmt"
	"syscall"

	"github.com/danieljoos/wincred"
	"github.com/zalando/go-keyring"
)

const windowsCredMaxBlobBytes = 2560

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
	if len(secret) > windowsCredMaxBlobBytes {
		return fmt.Errorf("credential exceeds Windows Credential Manager %d-byte limit: %w", windowsCredMaxBlobBytes, keyring.ErrSetDataTooBig)
	}
	targets := windowsCredTargetNames(service, user)
	chosen := targets[0]
	for _, target := range targets {
		if _, err := wincred.GetGenericCredential(target); err == nil {
			chosen = target
			break
		}
	}
	cred := wincred.NewGenericCredential(chosen)
	cred.UserName = user
	cred.CredentialBlob = []byte(secret)
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
