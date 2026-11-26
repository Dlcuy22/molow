//go:build !windows && !linux && !darwin

package backend

type unsupportedKeyStore struct{}

func newPlatformKeyStore() KeyStore {
	return unsupportedKeyStore{}
}

func (unsupportedKeyStore) Probe() error                    { return ErrKeyringUnavailable }
func (unsupportedKeyStore) GetKey() ([]byte, error)         { return nil, ErrKeyringUnavailable }
func (unsupportedKeyStore) GetOrCreateKey() ([]byte, error) { return nil, ErrKeyringUnavailable }
func (unsupportedKeyStore) DeleteKey() error                { return ErrKeyringUnavailable }
