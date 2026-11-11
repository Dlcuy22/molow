package backend

import (
	"context"
	"errors"

	ytm "github.com/dlcuy22/ytm-go"
)

// restoreYTM attaches a stored session to the provider on startup.
//
// The stored file is trusted for the client it builds, but not for whether the
// session still works: a revoked session answers HTTP 200 with a signed-out page,
// so only a request can tell. That check therefore runs in the background, after
// the window is up, and logs its verdict instead of blocking anything.
func (s *PlayerService) restoreYTM() {
	creds, err := loadCredentials()
	if err != nil {
		if !errors.Is(err, ErrNoCredentials) {
			s.log.Warn("ytm: stored session is unusable", "error", err)
		}
		return
	}

	client, err := clientFromCredentials(creds)
	if err != nil {
		s.log.Warn("ytm: could not rebuild the session", "error", err)
		return
	}
	s.ytmProv.setClient(client)
	s.log.Info("ytm: session restored", "account", creds.Name, "channel", creds.ChannelID)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ctx, cancel := context.WithTimeout(s.ctx, authProbeTimeout)
		defer cancel()

		if _, _, err := client.GetLikedSongs(ctx); err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				// The file stays: the user may want to copy the session out, and
				// deleting it silently would be worse than reporting it.
				s.log.Warn("ytm: the stored session has expired; sign in again")
				return
			}
			// A network failure says nothing about the session.
			s.log.Debug("ytm: session check skipped", "error", err)
			return
		}
		s.log.Debug("ytm: session check passed")
	}()
}

// setClient swaps in the client built for a signed-in session, and drops the
// lyric sheets because a different session can see different ones.
func (p *ytmProvider) setClient(client *ytm.Client) {
	if p == nil || client == nil {
		return
	}

	p.installClient(client)

	// A different session can see different lyrics, so the sheets resolved
	// under the previous one are dropped rather than reused.
	p.sheetMu.Lock()
	p.sheets = make(map[string]*ytm.Lyrics)
	p.sheetAt = nil
	p.sheetMu.Unlock()
}
