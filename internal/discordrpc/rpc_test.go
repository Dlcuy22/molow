package discordrpc

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBuildActivityFallsBackToAppAssets pins the empty-art case: a track with no
// cover or avatar uses the registered asset keys, so the presence still shows
// something rather than an empty frame.
func TestBuildActivityFallsBackToAppAssets(t *testing.T) {
	cfg := Config{
		LargeAssetKey:  "ongoplayer",
		LargeAssetText: "molo",
		SmallAssetKey:  "ongoplayer_small",
		SmallAssetText: "molo",
	}
	track := TrackInfo{Title: "A Song", Artist: "An Artist", ActivityType: 2}

	got := buildActivity(track, cfg, false, 0, 0)

	if got.LargeImage != "ongoplayer" || got.SmallImage != "ongoplayer_small" {
		t.Fatalf("assets = %q/%q, want the fallbacks", got.LargeImage, got.SmallImage)
	}
	if got.LargeText != "molo" {
		t.Fatalf("LargeText = %q, want the fallback text", got.LargeText)
	}
	if got.Details != "A Song" || got.State != "An Artist" {
		t.Fatalf("details/state = %q/%q", got.Details, got.State)
	}
}

// TestBuildActivityNamesTheArtist pins the card header: Discord renders
// "Listening to <Name>", so the artist must be the activity name, matching the
// reference client rather than the application name.
func TestBuildActivityNamesTheArtist(t *testing.T) {
	got := buildActivity(TrackInfo{Title: "A Song", Artist: "An Artist", ActivityType: 2}, Config{}, false, 0, 0)

	if got.Name != "An Artist" {
		t.Fatalf("Name = %q, want the artist", got.Name)
	}

	unnamed := buildActivity(TrackInfo{Title: "A Song"}, Config{}, false, 0, 0)
	if unnamed.Name != "Unknown artist" {
		t.Fatalf("Name = %q, want the unknown-artist fallback", unnamed.Name)
	}
}

// TestActivityWireCarriesTheName pins the wire shape: the name reaches the
// SET_ACTIVITY payload, because that is the field Discord draws in the header.
func TestActivityWireCarriesTheName(t *testing.T) {
	card := buildActivity(TrackInfo{Title: "A Song", Artist: "An Artist", ActivityType: 2}, Config{}, false, 0, 0)

	wire := card.wire()
	if wire == nil {
		t.Fatal("wire() returned nil for a track")
	}
	if wire.Name != "An Artist" {
		t.Fatalf("wire name = %q, want the artist", wire.Name)
	}
	if wire.Type != 2 {
		t.Fatalf("wire type = %d, want 2 (Listening)", wire.Type)
	}

	if (*activity)(nil).wire() != nil {
		t.Fatal("a nil activity must clear the presence")
	}
}

// TestBuildActivityPrefersTrackArt pins the opposite: real art wins over the
// fallback key.
func TestBuildActivityPrefersTrackArt(t *testing.T) {
	cfg := Config{LargeAssetKey: "ongoplayer", SmallAssetKey: "ongoplayer_small"}
	track := TrackInfo{
		Title:      "A Song",
		Artist:     "An Artist",
		LargeImage: "https://cover",
		SmallImage: "https://avatar",
	}

	got := buildActivity(track, cfg, false, 0, 0)

	if got.LargeImage != "https://cover" || got.SmallImage != "https://avatar" {
		t.Fatalf("assets = %q/%q, want the track's", got.LargeImage, got.SmallImage)
	}
}

// TestBuildActivityDropsClockWhenPaused pins the pause rule: no timestamps, so
// Discord draws no moving progress bar.
func TestBuildActivityDropsClockWhenPaused(t *testing.T) {
	got := buildActivity(TrackInfo{Title: "T"}, Config{}, true, 5000, 1000)
	if got.Timestamps != nil {
		t.Fatalf("paused activity carried timestamps: %+v", got.Timestamps)
	}
}

// TestBuildActivityClocksAnEndWhenDurationIsKnown pins the progress bar: a
// playing track with a duration gets both ends, an unknown duration only a
// start.
func TestBuildActivityClocksAnEndWhenDurationIsKnown(t *testing.T) {
	withEnd := buildActivity(TrackInfo{Title: "T"}, Config{}, false, 10000, 30000)
	if withEnd.Timestamps == nil || withEnd.Timestamps.Start == nil || withEnd.Timestamps.End == nil {
		t.Fatalf("expected both timestamps, got %+v", withEnd.Timestamps)
	}

	openEnded := buildActivity(TrackInfo{Title: "T"}, Config{}, false, 10000, 0)
	if openEnded.Timestamps == nil || openEnded.Timestamps.Start == nil {
		t.Fatalf("expected a start timestamp, got %+v", openEnded.Timestamps)
	}
	if openEnded.Timestamps.End != nil {
		t.Fatalf("unknown duration produced an end: %+v", openEnded.Timestamps.End)
	}
}

// TestCompleteButtonsDropsIncomplete pins the button rule: Discord rejects a
// button without both a label and a URL, so a half-built one is dropped.
func TestCompleteButtonsDropsIncomplete(t *testing.T) {
	got := completeButtons([]Button{
		{Label: "Ok", URL: "https://x"},
		{Label: "", URL: "https://y"},
		{Label: "No URL", URL: ""},
	})
	if len(got) != 1 {
		t.Fatalf("buttons = %d, want 1", len(got))
	}
	if got[0].Label != "Ok" || got[0].URL != "https://x" {
		t.Fatalf("button = %+v, want the complete one", got[0])
	}
}

// TestManagerLifecycleIsRaceFree exercises Start/Update/Stop and a restart
// while updates arrive from another goroutine. Discord is not running here, so
// the socket never opens; the point is that the manager's own state and channel
// handling survive the churn under -race and that Stop-then-Start is reusable.
func TestManagerLifecycleIsRaceFree(t *testing.T) {
	m := New(Config{AppID: "1"}, nil)
	m.GetPosition = func() int64 { return 1000 }
	m.GetDuration = func() int64 { return 30000 }
	m.IsPaused = func() bool { return false }

	stopUpdates := make(chan struct{})
	var updates sync.WaitGroup
	updates.Add(1)
	go func() {
		defer updates.Done()
		for {
			select {
			case <-stopUpdates:
				return
			default:
				m.Update(TrackInfo{Title: "A", Artist: "B", DurationMs: 1000})
				m.Clear()
			}
		}
	}()

	for i := 0; i < 3; i++ {
		m.Start()
		time.Sleep(20 * time.Millisecond)
		m.Stop()
	}

	close(stopUpdates)
	updates.Wait()
}

type trackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackingConn) Close() error {
	c.closed.Store(true)

	return c.Conn.Close()
}

func writeTestFrame(c net.Conn, op int, payload string) error {
	buf := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(op))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)
	_, err := c.Write(buf)

	return err
}

func startFakeDiscord(onSet func(server net.Conn, nonce string)) (func() (net.Conn, error), *[]*trackingConn) {
	var (
		mu    sync.Mutex
		conns []*trackingConn
	)
	dial := func() (net.Conn, error) {
		client, server := net.Pipe()
		tc := &trackingConn{Conn: client}
		mu.Lock()
		conns = append(conns, tc)
		mu.Unlock()

		go func() {
			defer server.Close()
			var hdr [8]byte
			if _, err := io.ReadFull(server, hdr[:]); err != nil {
				return
			}
			n := int(binary.LittleEndian.Uint32(hdr[4:8]))
			if _, err := io.ReadFull(server, make([]byte, n)); err != nil {
				return
			}
			if err := writeTestFrame(server, opFrame, `{"cmd":"DISPATCH","evt":"READY"}`); err != nil {
				return
			}

			for {
				if _, err := io.ReadFull(server, hdr[:]); err != nil {
					return
				}
				op := int(binary.LittleEndian.Uint32(hdr[0:4]))
				n = int(binary.LittleEndian.Uint32(hdr[4:8]))
				buf := make([]byte, n)
				if _, err := io.ReadFull(server, buf); err != nil {
					return
				}
				if op == opClose {
					return
				}
				var req struct {
					Nonce string `json:"nonce"`
				}
				_ = json.Unmarshal(buf, &req)
				onSet(server, req.Nonce)
			}
		}()

		return tc, nil
	}

	return dial, &conns
}

func TestMarkDisconnectedClosesPipeBeforeReconnect(t *testing.T) {
	var calls atomic.Int32
	dial, conns := startFakeDiscord(func(server net.Conn, nonce string) {
		if calls.Add(1) == 1 {
			_ = writeTestFrame(server, opFrame, `{"evt":"ERROR","nonce":"`+nonce+`","data":{"code":1000,"message":"fail"}}`)

			return
		}
		_ = writeTestFrame(server, opFrame, `{"cmd":"SET_ACTIVITY","nonce":"`+nonce+`"}`)
	})

	m := New(Config{AppID: "1"}, nil)
	m.discord.dial = dial
	defer m.disconnect()

	if err := m.connect(); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	m.current = TrackInfo{Title: "TUYU", Artist: "TUYU", DurationMs: 245000}
	m.setActivity()

	if m.Connected() {
		t.Fatal("expected manager disconnected after ERROR reply")
	}
	if len(*conns) != 1 || !(*conns)[0].closed.Load() {
		t.Fatal("failed SetActivity leaked the first IPC pipe without closing it")
	}

	if err := m.connect(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	m.current = TrackInfo{Title: "KICK BACK", Artist: "Kenshi Yonezu", DurationMs: 193000}
	m.setActivity()
	if !m.Connected() {
		t.Fatal("expected manager connected after reconnect")
	}
	if len(*conns) != 2 || !(*conns)[0].closed.Load() || (*conns)[1].closed.Load() {
		t.Fatalf("unexpected conn states: len=%d firstClosed=%v secondClosed=%v",
			len(*conns), (*conns)[0].closed.Load(), (*conns)[1].closed.Load())
	}
}

func TestSetActivitySkipsUnchangedAndSendsOnSeekOrPause(t *testing.T) {
	var sent atomic.Int32
	dial, _ := startFakeDiscord(func(server net.Conn, nonce string) {
		sent.Add(1)
		_ = writeTestFrame(server, opFrame, `{"cmd":"SET_ACTIVITY","nonce":"`+nonce+`"}`)
	})

	pos := int64(10_000)
	dur := int64(200_000)
	paused := false

	m := New(Config{AppID: "1"}, nil)
	m.discord.dial = dial
	m.GetPosition = func() int64 { return pos }
	m.GetDuration = func() int64 { return dur }
	m.IsPaused = func() bool { return paused }
	defer m.disconnect()

	if err := m.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	m.current = TrackInfo{Title: "KICK BACK", Artist: "Kenshi Yonezu"}
	m.setActivity()
	if sent.Load() != 1 {
		t.Fatalf("initial send count = %d, want 1", sent.Load())
	}

	// Unchanged tick should be skipped.
	m.setActivity()
	if sent.Load() != 1 {
		t.Fatalf("unchanged tick sent again: count = %d, want 1", sent.Load())
	}

	// Seek (>2s jump) should trigger a send.
	pos = 65_000
	m.setActivity()
	if sent.Load() != 2 {
		t.Fatalf("seek did not send: count = %d, want 2", sent.Load())
	}

	// Pause sends once, repeated paused tick is skipped.
	paused = true
	m.setActivity()
	m.setActivity()
	if sent.Load() != 3 {
		t.Fatalf("pause send count = %d, want 3", sent.Load())
	}

	// Clear sends once, repeated clear is skipped.
	m.current = TrackInfo{}
	m.setActivity()
	m.setActivity()
	if sent.Load() != 4 {
		t.Fatalf("clear send count = %d, want 4", sent.Load())
	}
}

func TestAwaitSkipsUnsolicitedDispatchUntilMatchingNonce(t *testing.T) {
	dial, _ := startFakeDiscord(func(server net.Conn, nonce string) {
		_ = writeTestFrame(server, opFrame, `{"cmd":"DISPATCH","evt":"ACTIVITY_JOIN"}`)
		_ = writeTestFrame(server, opFrame, `{"cmd":"SET_ACTIVITY","nonce":"other-nonce"}`)
		_ = writeTestFrame(server, opFrame, `{"cmd":"SET_ACTIVITY","nonce":"`+nonce+`"}`)
	})

	c := newIPCClient("1")
	c.dial = dial
	defer c.close()

	if err := c.login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := c.setActivity(&activity{Details: "KICK BACK", State: "Kenshi Yonezu"}); err != nil {
		t.Fatalf("setActivity: %v", err)
	}
}
