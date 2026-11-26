package backend

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dlcuy22/molow/internal/library"
	ytm "github.com/dlcuy22/ytm-go"
)

func TestYTMSyncerTickFocusAndCoalesce(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var lastSync int64

	tickCh := make(chan time.Time)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var runs atomic.Int32

	s := newYTMSyncer(context.Background(), ytmSyncerConfig{
		id:     "ytm:test",
		nowFn:  func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		tickCh: tickCh,
		lastSyncFn: func() int64 {
			mu.Lock()
			defer mu.Unlock()
			return lastSync
		},
		runFn: func(context.Context) error {
			n := runs.Add(1)
			mu.Lock()
			lastSync = now.Unix()
			mu.Unlock()
			entered <- struct{}{}
			if n == 1 {
				<-release
			}
			return nil
		},
	})
	defer s.stop()

	// Trigger run 1 via tick and queue two overlapping triggers (focus + tick) while blocked.
	tickCh <- now
	<-entered
	mu.Lock()
	now = now.Add(6 * time.Minute)
	mu.Unlock()
	s.trigger(syncTriggerFocus)
	s.trigger(syncTriggerTick)
	close(release)

	// Exactly one coalesced follow-up run must execute.
	<-entered
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2 (initial + coalesced rerun)", got)
	}

	// Focus within 5m is skipped; focus after >5m runs.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	s.trigger(syncTriggerFocus)
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("focus within 5m ran unexpectedly: runs=%d", got)
	}

	mu.Lock()
	now = now.Add(4 * time.Minute)
	mu.Unlock()
	s.trigger(syncTriggerFocus)
	<-entered
	if got := runs.Load(); got != 3 {
		t.Fatalf("focus after >5m: runs=%d, want 3", got)
	}
}

func TestYTMSyncerBackoffResetAndExpired(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	tickCh := make(chan time.Time)
	doneRun := make(chan error, 8)
	var nextErr atomic.Value
	nextErr.Store(errors.New("network down"))
	var passNext atomic.Bool

	var errEvents atomic.Int32
	s := newYTMSyncer(context.Background(), ytmSyncerConfig{
		id:     "ytm:test",
		nowFn:  func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		tickCh: tickCh,
		runFn: func(context.Context) error {
			if passNext.Load() {
				doneRun <- nil
				return nil
			}
			err := nextErr.Load().(error)
			doneRun <- err
			return err
		},
		onErrorFn: func() { errEvents.Add(1) },
	})
	defer s.stop()

	// Run 1 fails -> backoff = 1m.
	tickCh <- now
	<-doneRun
	time.Sleep(20 * time.Millisecond)
	until, lastErr := s.backoffStatus()
	if lastErr == nil || !until.Equal(now.Add(1*time.Minute)) {
		t.Fatalf("1st backoff = (%v, %v), want +1m", until, lastErr)
	}
	if errEvents.Load() != 1 {
		t.Fatalf("expected error event emitted")
	}

	// Tick while in backoff is skipped.
	advance(30 * time.Second)
	tickCh <- now
	select {
	case <-doneRun:
		t.Fatal("tick inside backoff should be skipped")
	case <-time.After(30 * time.Millisecond):
	}

	// Advance past 1m -> run 2 fails -> backoff doubles to 2m.
	advance(31 * time.Second)
	tickCh <- now
	<-doneRun
	time.Sleep(20 * time.Millisecond)
	until, _ = s.backoffStatus()
	mu.Lock()
	want2 := now.Add(2 * time.Minute)
	mu.Unlock()
	if !until.Equal(want2) {
		t.Fatalf("2nd backoff = %v, want %v (+2m)", until, want2)
	}

	// Manual trigger clears backoff immediately and succeeds -> resets to zero.
	passNext.Store(true)
	s.trigger(syncTriggerManual)
	<-doneRun
	time.Sleep(20 * time.Millisecond)
	until, lastErr = s.backoffStatus()
	if lastErr != nil || !until.IsZero() {
		t.Fatalf("expected reset after manual success, got (%v, %v)", until, lastErr)
	}

	// Session expired does not schedule retry backoff.
	nextErr.Store(ytm.ErrSessionExpired)
	passNext.Store(false)
	tickCh <- now
	<-doneRun
	time.Sleep(20 * time.Millisecond)
	until, lastErr = s.backoffStatus()
	if lastErr != nil || !until.IsZero() {
		t.Fatalf("session expired should park without error backoff, got (%v, %v)", until, lastErr)
	}
}

func TestYTMSourceDescribeBackoffStatus(t *testing.T) {
	svc, _ := newYTMTestService(t)
	creds := &Credentials{CookieHeader: "SAPISID=s; SID=id", ChannelID: "UCerr", Name: "Err"}
	if err := saveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	libID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatal(err)
	}
	src := svc.libs[libID].(*ytmSource)
	src.syncer.mu.Lock()
	src.syncer.lastErr = errors.New("boom")
	src.syncer.backoffUntil = time.Now().Add(3 * time.Minute)
	src.syncer.mu.Unlock()

	lib, err := src.Describe(context.Background())
	if err != nil || lib.Status != library.StatusError || !strings.Contains(lib.Message, "retrying in") {
		t.Fatalf("Describe = (%+v, %v)", lib, err)
	}
}
