//go:build windows

package backend

import (
	"crypto/rand"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric          = 1
	credPersistLocalMachine  = 2
	errNotFound              = windows.ERROR_NOT_FOUND
	defaultKeyringTargetName = "molow:ytm-credentials-key"
)

var (
	modAdvapi32    = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = modAdvapi32.NewProc("CredReadW")
	procCredWriteW = modAdvapi32.NewProc("CredWriteW")
	procCredFree   = modAdvapi32.NewProc("CredFree")
	procCredDelete = modAdvapi32.NewProc("CredDeleteW")
)

type winCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// WindowsCredentialStore stores the 32-byte AES-256 key in Windows Credential
// Manager (well below the 2,560-byte CRED_MAX_CREDENTIAL_BLOB_SIZE limit).
type WindowsCredentialStore struct {
	TargetName string
}

// NewWindowsCredentialStore builds a Windows Credential Manager store for
// targetName, or the default production target when targetName is empty.
func NewWindowsCredentialStore(targetName string) *WindowsCredentialStore {
	if targetName == "" {
		targetName = defaultKeyringTargetName
	}
	return &WindowsCredentialStore{TargetName: targetName}
}

func newPlatformKeyStore() KeyStore {
	return NewWindowsCredentialStore("")
}

// Probe verifies Windows Credential Manager is reachable before starting sign-in.
func (s *WindowsCredentialStore) Probe() error {
	_, err := s.GetKey()
	if err == nil || errors.Is(err, ErrKeyNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
}

func (s *WindowsCredentialStore) GetKey() ([]byte, error) {
	target, err := windows.UTF16PtrFromString(s.TargetName)
	if err != nil {
		return nil, err
	}

	var cred *winCredential
	ok, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if ok == 0 {
		if errors.Is(callErr, errNotFound) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("CredReadW(%q): %w", s.TargetName, callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))

	if cred.CredentialBlobSize != aes256KeySize || cred.CredentialBlob == nil {
		return nil, fmt.Errorf("keyring entry %q has unexpected key length %d", s.TargetName, cred.CredentialBlobSize)
	}
	key := make([]byte, aes256KeySize)
	copy(key, unsafe.Slice(cred.CredentialBlob, aes256KeySize))
	return key, nil
}

func (s *WindowsCredentialStore) GetOrCreateKey() ([]byte, error) {
	if key, err := s.GetKey(); err == nil {
		return key, nil
	} else if !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}

	key := make([]byte, aes256KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate AES-256 key: %w", err)
	}
	if err := s.writeKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *WindowsCredentialStore) writeKey(key []byte) error {
	target, err := windows.UTF16PtrFromString(s.TargetName)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString("molow")
	if err != nil {
		return err
	}
	comment, err := windows.UTF16PtrFromString("Molow YTM envelope key")
	if err != nil {
		return err
	}

	cred := winCredential{
		Type:               credTypeGeneric,
		TargetName:         target,
		Comment:            comment,
		CredentialBlobSize: uint32(len(key)),
		CredentialBlob:     &key[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}

	ok, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if ok == 0 {
		return fmt.Errorf("CredWriteW(%q): %w", s.TargetName, callErr)
	}
	return nil
}

func (s *WindowsCredentialStore) DeleteKey() error {
	target, err := windows.UTF16PtrFromString(s.TargetName)
	if err != nil {
		return err
	}
	ok, _, callErr := procCredDelete.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
	)
	if ok == 0 {
		if errors.Is(callErr, errNotFound) {
			return nil
		}
		return fmt.Errorf("CredDeleteW(%q): %w", s.TargetName, callErr)
	}
	return nil
}
