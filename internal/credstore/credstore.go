// Package credstore stores and retrieves PTV credentials in the OS-native
// secret store (macOS Keychain, Windows Credential Manager, Linux Secret
// Service) via go-keyring.
package credstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// service is the keyring service name under which credentials are stored.
const service = "ptv-cli"

const (
	keyAPIKey = "PTV_API_KEY"
	keyDevID  = "PTV_API_USERID"
)

// ErrNotFound indicates no credentials are stored in the keyring.
var ErrNotFound = errors.New("no credentials stored in OS keyring")

// Credentials holds a PTV API key and developer id.
type Credentials struct {
	APIKey string
	DevID  string
}

// Save writes credentials to the OS keyring.
func Save(c Credentials) error {
	if err := keyring.Set(service, keyAPIKey, c.APIKey); err != nil {
		return err
	}
	return keyring.Set(service, keyDevID, c.DevID)
}

// Load reads credentials from the OS keyring. Returns ErrNotFound if either
// value is missing.
func Load() (Credentials, error) {
	apiKey, err := keyring.Get(service, keyAPIKey)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Credentials{}, ErrNotFound
		}
		return Credentials{}, err
	}
	devID, err := keyring.Get(service, keyDevID)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Credentials{}, ErrNotFound
		}
		return Credentials{}, err
	}
	return Credentials{APIKey: apiKey, DevID: devID}, nil
}

// Delete removes any stored credentials. Missing entries are not an error.
func Delete() error {
	var errs []error
	if err := keyring.Delete(service, keyAPIKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, err)
	}
	if err := keyring.Delete(service, keyDevID); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
