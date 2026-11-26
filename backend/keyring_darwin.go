//go:build darwin

package backend

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	keychainService = "molow"
	keychainAccount = "ytm-credentials-key"
)

type darwinKeychainStore struct{}

func newPlatformKeyStore() KeyStore {
	return &darwinKeychainStore{}
}

func (s *darwinKeychainStore) Probe() error {
	cmd := exec.Command("/usr/bin/security", "default-keychain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: macOS Keychain unavailable (%v: %s)", ErrKeyringUnavailable, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *darwinKeychainStore) GetKey() ([]byte, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "could not be found") || strings.Contains(msg, "secitemnotfound") {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("security find-generic-password: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	key, decErr := hex.DecodeString(strings.TrimSpace(string(out)))
	if decErr != nil || len(key) != aes256KeySize {
		return nil, fmt.Errorf("keychain entry %q has invalid key data", keychainService)
	}
	return key, nil
}

func (s *darwinKeychainStore) GetOrCreateKey() ([]byte, error) {
	if key, err := s.GetKey(); err == nil {
		return key, nil
	} else if !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}

	key := make([]byte, aes256KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate AES-256 key: %w", err)
	}
	encoded := hex.EncodeToString(key)
	cmd := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", keychainService, "-a", keychainAccount, "-w", encoded)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w: security add-generic-password: %v (%s)", ErrKeyringUnavailable, err, strings.TrimSpace(string(out)))
	}
	return key, nil
}

func (s *darwinKeychainStore) DeleteKey() error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-s", keychainService, "-a", keychainAccount)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "could not be found") || strings.Contains(msg, "secitemnotfound") {
			return nil
		}
		return fmt.Errorf("security delete-generic-password: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
