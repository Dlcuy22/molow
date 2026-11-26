//go:build linux

package backend

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	ssBusName            = "org.freedesktop.secrets"
	ssServicePath        = dbus.ObjectPath("/org/freedesktop/secrets")
	ssDefaultCollection  = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	ssServiceIface       = "org.freedesktop.Secret.Service"
	ssCollectionIface    = "org.freedesktop.Secret.Collection"
	ssItemIface          = "org.freedesktop.Secret.Item"
	ssSessionIface       = "org.freedesktop.Secret.Session"
	ssKeyLabel           = "Molow YouTube Music key"
	ssKeyAttrApplication = "molow"
	ssKeyAttrAccount     = "ytm-credentials-key"
)

type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type secretServiceStore struct{}

func newPlatformKeyStore() KeyStore {
	return &secretServiceStore{}
}

func ssAttributes() map[string]string {
	return map[string]string{
		"application": ssKeyAttrApplication,
		"account":     ssKeyAttrAccount,
	}
}

func openSecretSession() (*dbus.Conn, dbus.ObjectPath, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, "", fmt.Errorf("%w: D-Bus session bus unavailable (%v); start a Secret Service daemon such as GNOME Keyring or KWallet", ErrKeyringUnavailable, err)
	}

	var hasOwner bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, ssBusName).Store(&hasOwner); err != nil {
		return nil, "", fmt.Errorf("%w: failed to query Secret Service (%v); GNOME Keyring or KWallet is required", ErrKeyringUnavailable, err)
	}
	if !hasOwner {
		return nil, "", fmt.Errorf("%w: no Secret Service daemon (%s) is running; GNOME Keyring or KWallet is required", ErrKeyringUnavailable, ssBusName)
	}

	svc := conn.Object(ssBusName, ssServicePath)
	var ignored dbus.Variant
	var session dbus.ObjectPath
	if err := svc.Call(ssServiceIface+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&ignored, &session); err != nil {
		return nil, "", fmt.Errorf("%w: cannot open Secret Service session (%v)", ErrKeyringUnavailable, err)
	}
	return conn, session, nil
}

func closeSecretSession(conn *dbus.Conn, session dbus.ObjectPath) {
	if conn == nil || session == "" || session == "/" {
		return
	}
	_ = conn.Object(ssBusName, session).Call(ssSessionIface+".Close", 0).Err
}

// Probe verifies a Secret Service daemon is running and its default collection
// is unlocked and usable before starting sign-in.
func (s *secretServiceStore) Probe() error {
	conn, session, err := openSecretSession()
	if err != nil {
		return err
	}
	defer closeSecretSession(conn, session)

	col := conn.Object(ssBusName, ssDefaultCollection)
	lockedVar, err := col.GetProperty(ssCollectionIface + ".Locked")
	if err != nil {
		return fmt.Errorf("%w: default keyring collection unavailable (%v)", ErrKeyringUnavailable, err)
	}
	if locked, ok := lockedVar.Value().(bool); ok && locked {
		var unlocked []dbus.ObjectPath
		var prompt dbus.ObjectPath
		if err := conn.Object(ssBusName, ssServicePath).Call(
			ssServiceIface+".Unlock",
			0,
			[]dbus.ObjectPath{ssDefaultCollection},
		).Store(&unlocked, &prompt); err != nil || len(unlocked) == 0 {
			return fmt.Errorf("%w: system keyring is locked; unlock GNOME Keyring or KWallet and try again", ErrKeyringUnavailable)
		}
	}
	return nil
}

func (s *secretServiceStore) findItem(conn *dbus.Conn) (dbus.ObjectPath, error) {
	col := conn.Object(ssBusName, ssDefaultCollection)
	var items []dbus.ObjectPath
	if err := col.Call(ssCollectionIface+".SearchItems", 0, ssAttributes()).Store(&items); err != nil {
		return "", fmt.Errorf("%w: search keyring items (%v)", ErrKeyringUnavailable, err)
	}
	if len(items) == 0 {
		return "", ErrKeyNotFound
	}
	return items[0], nil
}

func (s *secretServiceStore) GetKey() ([]byte, error) {
	conn, session, err := openSecretSession()
	if err != nil {
		return nil, err
	}
	defer closeSecretSession(conn, session)

	itemPath, err := s.findItem(conn)
	if err != nil {
		return nil, err
	}

	var sec ssSecret
	if err := conn.Object(ssBusName, itemPath).Call(ssItemIface+".GetSecret", 0, session).Store(&sec); err != nil {
		return nil, fmt.Errorf("%w: read keyring secret (%v)", ErrKeyringUnavailable, err)
	}

	if len(sec.Value) == aes256KeySize {
		key := make([]byte, aes256KeySize)
		copy(key, sec.Value)
		return key, nil
	}
	key, decErr := base64.StdEncoding.DecodeString(string(sec.Value))
	if decErr != nil || len(key) != aes256KeySize {
		return nil, fmt.Errorf("keyring entry has unexpected key length")
	}
	return key, nil
}

func (s *secretServiceStore) GetOrCreateKey() ([]byte, error) {
	if key, err := s.GetKey(); err == nil {
		return key, nil
	} else if !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}

	if err := s.Probe(); err != nil {
		return nil, err
	}

	key := make([]byte, aes256KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate AES-256 key: %w", err)
	}

	conn, session, err := openSecretSession()
	if err != nil {
		return nil, err
	}
	defer closeSecretSession(conn, session)

	props := map[string]dbus.Variant{
		ssItemIface + ".Label":      dbus.MakeVariant(ssKeyLabel),
		ssItemIface + ".Attributes": dbus.MakeVariant(ssAttributes()),
	}
	sec := ssSecret{
		Session:     session,
		Value:       []byte(base64.StdEncoding.EncodeToString(key)),
		ContentType: "text/plain; charset=utf8",
	}

	var created dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := conn.Object(ssBusName, ssDefaultCollection).Call(
		ssCollectionIface+".CreateItem",
		0,
		props,
		sec,
		true,
	).Store(&created, &prompt); err != nil {
		return nil, fmt.Errorf("%w: write keyring secret (%v)", ErrKeyringUnavailable, err)
	}
	if created == "" || created == "/" {
		return nil, fmt.Errorf("%w: keyring creation requires interactive prompt unlock", ErrKeyringUnavailable)
	}
	return key, nil
}

func (s *secretServiceStore) DeleteKey() error {
	conn, session, err := openSecretSession()
	if err != nil {
		return err
	}
	defer closeSecretSession(conn, session)

	itemPath, err := s.findItem(conn)
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			return nil
		}
		return err
	}

	var prompt dbus.ObjectPath
	if err := conn.Object(ssBusName, itemPath).Call(ssItemIface+".Delete", 0).Store(&prompt); err != nil {
		return fmt.Errorf("%w: delete keyring secret (%v)", ErrKeyringUnavailable, err)
	}
	return nil
}
