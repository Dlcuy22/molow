package backend

import (
	"context"
	"errors"
	"sync"
	"time"

	ytm "github.com/dlcuy22/ytm-go"
)

type syncTrigger int

const (
	syncTriggerTick syncTrigger = iota
	syncTriggerFocus
	syncTriggerOpen
	syncTriggerManual
)

type ytmSyncerConfig struct {
	id         string
	nowFn      func() time.Time
	tickCh     <-chan time.Time
	lastSyncFn func() int64
	canSyncFn  func() bool
	runFn      func(context.Context) error
	onErrorFn  func()
}

// ytmSyncer serializes Stage 1/2 refreshes per YTM library, coalescing
// concurrent triggers and backing off exponentially after transient failures.
type ytmSyncer struct {
	cfg ytmSyncerConfig

	triggers chan syncTrigger
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once

	mu           sync.Mutex
	running      bool
	rerun        bool
	rerunKind    syncTrigger
	backoffDelay time.Duration
	backoffUntil time.Time
	lastErr      error
}

func newYTMSyncer(ctx context.Context, cfg ytmSyncerConfig) *ytmSyncer {
	if cfg.nowFn == nil {
		cfg.nowFn = time.Now
	}
	s := &ytmSyncer{
		cfg:      cfg,
		triggers: make(chan syncTrigger, 4),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go s.loop(ctx)
	return s
}

func (s *ytmSyncer) trigger(kind syncTrigger) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if kind == syncTriggerManual {
		s.backoffDelay = 0
		s.backoffUntil = time.Time{}
		s.lastErr = nil
	}
	if s.running {
		if !s.rerun || kind > s.rerunKind {
			s.rerun = true
			s.rerunKind = kind
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	select {
	case s.triggers <- kind:
	default:
	}
}

func (s *ytmSyncer) stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	<-s.doneCh
}

func (s *ytmSyncer) backoffStatus() (until time.Time, lastErr error) {
	if s == nil {
		return time.Time{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backoffUntil, s.lastErr
}

func (s *ytmSyncer) shouldRun(kind syncTrigger) bool {
	if s.cfg.canSyncFn != nil && !s.cfg.canSyncFn() {
		return false
	}
	now := s.cfg.nowFn()
	s.mu.Lock()
	until := s.backoffUntil
	s.mu.Unlock()

	if kind != syncTriggerManual && !until.IsZero() && now.Before(until) {
		return false
	}
	if kind == syncTriggerFocus && s.cfg.lastSyncFn != nil {
		if at := s.cfg.lastSyncFn(); at != 0 && now.Sub(time.Unix(at, 0)) <= ytmFocusStaleAge {
			return false
		}
	}
	if kind == syncTriggerOpen && s.cfg.lastSyncFn != nil {
		if at := s.cfg.lastSyncFn(); at != 0 && now.Sub(time.Unix(at, 0)) <= ytmOpenStaleAge {
			return false
		}
	}
	return true
}

func (s *ytmSyncer) execute(ctx context.Context, kind syncTrigger) {
	if !s.shouldRun(kind) {
		return
	}

	s.mu.Lock()
	if s.running {
		if !s.rerun || kind > s.rerunKind {
			s.rerun = true
			s.rerunKind = kind
		}
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	for {
		err := s.cfg.runFn(ctx)

		s.mu.Lock()
		if err == nil {
			s.backoffDelay = 0
			s.backoffUntil = time.Time{}
			s.lastErr = nil
		} else if !errors.Is(err, ytm.ErrSessionExpired) {
			if s.backoffDelay <= 0 {
				s.backoffDelay = ytmBackoffInitial
			} else {
				s.backoffDelay *= 2
				if s.backoffDelay > ytmBackoffMax {
					s.backoffDelay = ytmBackoffMax
				}
			}
			s.backoffUntil = s.cfg.nowFn().Add(s.backoffDelay)
			s.lastErr = err
		}
		rerun := s.rerun
		nextKind := s.rerunKind
		s.rerun = false
		s.rerunKind = syncTriggerTick

		// Only run an immediate follow-up if the current pass succeeded or
		// the pending trigger was a manual retry.
		if !rerun || (err != nil && nextKind != syncTriggerManual) {
			s.running = false
			s.mu.Unlock()
			if err != nil && !errors.Is(err, ytm.ErrSessionExpired) && s.cfg.onErrorFn != nil {
				s.cfg.onErrorFn()
			}
			return
		}
		s.mu.Unlock()

		if !s.shouldRun(nextKind) {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		}
	}
}

func (s *ytmSyncer) loop(ctx context.Context) {
	defer close(s.doneCh)

	tickCh := s.cfg.tickCh
	var ticker *time.Ticker
	if tickCh == nil {
		ticker = time.NewTicker(ytmTickInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}

	for {
		select {
		case <-s.stopCh:
			return
		case <-done:
			return
		case <-tickCh:
			s.execute(ctx, syncTriggerTick)
		case kind := <-s.triggers:
			s.execute(ctx, kind)
		}
	}
}
