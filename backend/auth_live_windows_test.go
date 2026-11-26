//go:build windows

package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveExperimentCredentials verifies the production envelope decryption,
// Windows Credential Manager store, and ytm-go session against the existing
// signed-in session created by experiments/win-ytm-login.
func TestLiveExperimentCredentials(t *testing.T) {
	if os.Getenv("YTM_EXP_LIVE") == "" {
		t.Skip("set YTM_EXP_LIVE=1 to run against stored experiment credentials")
	}
	appData, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	credPath := filepath.Join(appData, "molow-win-ytm-exp", "credentials.enc")
	ks := NewWindowsCredentialStore("molow-exp:ytm-credentials-key")

	creds, err := loadCredentialsFrom(credPath, ks)
	if err != nil {
		t.Fatalf("loadCredentialsFrom(%s): %v", credPath, err)
	}
	client, err := clientFromCredentials(creds)
	if err != nil {
		t.Fatalf("clientFromCredentials: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	songs, _, err := client.GetLikedSongs(ctx)
	if err != nil {
		t.Fatalf("GetLikedSongs: %v", err)
	}
	t.Logf("verified stored experiment session: %s <%s>, %d liked songs (page 1)", creds.Name, creds.Email, len(songs))
}
