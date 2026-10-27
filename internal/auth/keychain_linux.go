//go:build linux

package auth

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo"
)

func secretError(operation string, err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("%s: the desktop Secret Service is unavailable. Start GNOME Keyring or KWallet and install libsecret (libsecret-1-0 on Debian and Ubuntu)", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// The service matches the macOS and Windows stores and mygo.json, so the
// name MyGo would derive at different points in startup cannot split it.
var secretStore = mygo.Secrets.Service("co.nggalek.titen")

func SetSecret(key, value string) error {
	if err := secretStore.Set(key, []byte(value)); err != nil {
		return secretError("save the token in the desktop Secret Service", err)
	}
	return nil
}

func GetSecret(key string) (string, error) {
	value, err := secretStore.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", secretError("read the token from the desktop Secret Service", err)
	}
	return string(value), nil
}
