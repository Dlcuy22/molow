package backend

import (
	"runtime"
	"testing"
	"time"

	"github.com/dlcuy22/molow/internal/romanize"
)

func TestRomanizerLoadsOnEnableAndUnloadsIdle(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	if svc.romanizer.engine != nil {
		t.Fatal("a service with romanization off loaded the dictionary at startup")
	}

	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	svc.syncRomanizer()

	engine := svc.romanizer.ready()
	if engine == nil {
		t.Fatal("enabling the setting did not produce an engine")
	}
	res := engine.Romanize("涙はそっと", nil)
	if res.Line != "namidawasotto" {
		t.Fatalf("engine converted to %q", res.Line)
	}
	if svc.romanizer.cachePath == "" {
		t.Fatal("no cache path, so the engine could never be unloaded")
	}
	if !romanize.CacheValid(svc.romanizer.cachePath) {
		t.Fatalf("cache at %q is not usable", svc.romanizer.cachePath)
	}

	svc.appSettings.Playback.Lyrics.AutoRomanization = false
	svc.syncRomanizer()
	svc.romanizer.releaseAfter(20 * time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.romanizer.mu.Lock()
		gone := svc.romanizer.engine == nil
		svc.romanizer.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the dictionary was not dropped after the idle window")
}

func TestRomanizerReenableCancelsUnload(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	svc.syncRomanizer()

	first := svc.romanizer.ready()
	if first == nil {
		t.Fatal("no engine after enabling")
	}

	svc.appSettings.Playback.Lyrics.AutoRomanization = false
	svc.syncRomanizer()
	svc.romanizer.releaseAfter(80 * time.Millisecond)

	time.Sleep(20 * time.Millisecond)
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.syncRomanizer()

	time.Sleep(200 * time.Millisecond)

	svc.romanizer.mu.Lock()
	still := svc.romanizer.engine
	svc.romanizer.mu.Unlock()
	if still != first {
		t.Fatal("re-enabling inside the grace period did not keep the engine")
	}
	if got := still.Romanize("涙はそっと", nil).Line; got != "namidawasotto" {
		t.Fatalf("the kept engine converted to %q", got)
	}
}

func TestRomanizerOffAtStartupParsesNothing(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	svc.appSettings.Playback.Lyrics.AutoRomanization = false
	svc.syncRomanizer()

	svc.romanizer.mu.Lock()
	engine, loading := svc.romanizer.engine, svc.romanizer.loading
	svc.romanizer.mu.Unlock()
	if engine != nil || loading != nil {
		t.Fatal("a service with the setting off started a dictionary load")
	}
}

func TestRomanizerReservedLanguageParsesNothing(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = "ko"
	svc.syncRomanizer()

	svc.romanizer.mu.Lock()
	engine := svc.romanizer.engine
	svc.romanizer.mu.Unlock()
	if engine != nil {
		t.Fatal("a language with no engine loaded the Japanese dictionary")
	}
}

func TestRomanizerShutdownPreventsLoad(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	svc.romanizer.shutdown()

	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	svc.syncRomanizer()

	if e := svc.romanizer.ready(); e != nil {
		t.Fatal("ready returned an engine after shutdown")
	}
}

func TestRomanizerStaleUnloadIgnored(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	svc := newPlayerService()
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	svc.syncRomanizer()

	eng := svc.romanizer.ready()
	if eng == nil {
		t.Fatal("no engine")
	}

	svc.romanizer.mu.Lock()
	staleGen := svc.romanizer.unloadGen
	svc.romanizer.mu.Unlock()

	svc.romanizer.hold()
	svc.appSettings.Playback.Lyrics.AutoRomanization = false
	svc.romanizer.unload(staleGen)

	svc.romanizer.mu.Lock()
	still := svc.romanizer.engine
	svc.romanizer.mu.Unlock()
	if still != eng {
		t.Fatal("stale unload callback dropped a newer engine")
	}
}

// isolateCache redirects os.UserCacheDir on Linux, Windows, and macOS.
func isolateCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("LocalAppData", dir)
	t.Setenv("HOME", dir)

	return dir
}

func heapMB() int {
	var m runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&m)

	return int(m.HeapAlloc >> 20)
}
