//go:build darwin && cgo

package desktopenv

import (
	"errors"

	keychain "github.com/keybase/go-keychain"
)

type platformTokenStore struct{}

func newTokenStore(string) tokenStore { return platformTokenStore{} }

func (platformTokenStore) Get(id string) (string, error) {
	data, err := keychain.GetGenericPassword("Brigade Remote Environments", id, "", "")
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return "", nil
	}
	return string(data), err
}

func (platformTokenStore) Set(id, token string) error {
	_ = keychain.DeleteGenericPasswordItem("Brigade Remote Environments", id)
	if token == "" {
		return nil
	}
	item := keychain.NewGenericPassword("Brigade Remote Environments", id, "Brigade remote refresh token", []byte(token), "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleAfterFirstUnlockThisDeviceOnly)
	return keychain.AddItem(item)
}

func (platformTokenStore) Delete(id string) error {
	err := keychain.DeleteGenericPasswordItem("Brigade Remote Environments", id)
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return nil
	}
	return err
}
