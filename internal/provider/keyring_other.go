//go:build !darwin

package provider

import (
	"errors"

	"github.com/zalando/go-keyring"
)

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (osKeyring) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret)
}

func (osKeyring) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
