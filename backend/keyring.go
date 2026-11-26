// Cross-platform OS keyring interface for the envelope encryption key.
//
// Credentials are encrypted on disk with AES-256-GCM; only the 32-byte master
// key lives in the OS keyring. This avoids Windows Credential Manager's 2,560-byte
// entry limit while keeping every secret off plaintext disk files.
package backend

import (
	"errors"
	"sync"
	"testing"
)

const aes256KeySize = 32

var (
	// ErrKeyNotFound means the OS keyring is reachable, but no master key has
	// been created yet (the normal state before signing in).
	ErrKeyNotFound = errors.New("keyring: master key not found")

	// ErrKeyringUnavailable means no OS secret store daemon or keychain is
	// available to protect credentials.
	ErrKeyringUnavailable = errors.New("keyring: system keyring is unavailable")
)

// KeyStore abstracts OS keyring key operations so unit tests can isolate
// themselves from the user's real Credential Manager / Secret Service / Keychain.
type KeyStore interface {
	Probe() error
	GetKey() ([]byte, error)
	GetOrCreateKey() ([]byte, error)
	DeleteKey() error
}

var (
	keyStoreMu     sync.Mutex
	activeKeyStore KeyStore
)

func getKeyStore() KeyStore {
	keyStoreMu.Lock()
	defer keyStoreMu.Unlock()
	if activeKeyStore == nil {
		activeKeyStore = newPlatformKeyStore()
	}
	return activeKeyStore
}

// setKeyStoreForTest replaces the active keyring for the duration of t so no
// unit test ever reads or mutates real OS keyring entries.
func setKeyStoreForTest(t testing.TB, ks KeyStore) {
	t.Helper()
	keyStoreMu.Lock()
	prev := activeKeyStore
	activeKeyStore = ks
	keyStoreMu.Unlock()
	t.Cleanup(func() {
		keyStoreMu.Lock()
		activeKeyStore = prev
		keyStoreMu.Unlock()
	})
}
