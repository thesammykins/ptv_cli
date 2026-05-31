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
	keyAPIKey        = "PTV_API_KEY"
	keyDevID         = "PTV_API_USERID"
	keyOpenDataKeyID = "PTV_OPENDATA_KEY_ID"
	keyOpenDataAPIID = "PTV_OPENDATA_API_ID"
)

// ErrNotFound indicates no credentials are stored in the keyring.
var ErrNotFound = errors.New("no credentials stored in OS keyring")

// Credentials holds a PTV API key and developer id.
type Credentials struct {
	APIKey string
	DevID  string
}

// OpenDataCredentials holds optional Transport Victoria Open Data credentials.
type OpenDataCredentials struct {
	KeyID string
	APIID string
}

// Save writes credentials to the OS keyring.
func Save(c Credentials) error {
	if err := keyring.Set(service, keyAPIKey, c.APIKey); err != nil {
		return err
	}
	return keyring.Set(service, keyDevID, c.DevID)
}

// SaveOpenData writes Transport Victoria Open Data credentials to the OS keyring.
func SaveOpenData(c OpenDataCredentials) error {
	if err := keyring.Set(service, keyOpenDataKeyID, c.KeyID); err != nil {
		return err
	}
	if c.APIID == "" {
		err := keyring.Delete(service, keyOpenDataAPIID)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(service, keyOpenDataAPIID, c.APIID)
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

// LoadOpenData reads Transport Victoria Open Data credentials from the keyring.
// Returns ErrNotFound when the required subscription key is missing.
func LoadOpenData() (OpenDataCredentials, error) {
	keyID, err := keyring.Get(service, keyOpenDataKeyID)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return OpenDataCredentials{}, ErrNotFound
		}
		return OpenDataCredentials{}, err
	}
	apiID, err := keyring.Get(service, keyOpenDataAPIID)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return OpenDataCredentials{}, err
	}
	return OpenDataCredentials{KeyID: keyID, APIID: apiID}, nil
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

// DeleteOpenData removes stored Transport Victoria Open Data credentials.
func DeleteOpenData() error {
	var errs []error
	for _, key := range []string{keyOpenDataKeyID, keyOpenDataAPIID} {
		if err := keyring.Delete(service, key); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
