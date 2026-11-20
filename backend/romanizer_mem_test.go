package backend

import (
	"testing"
	"time"
)

func TestRomanizerSecondRunUnloadReturnsMemory(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	first := newPlayerService()
	first.appSettings.Playback.Lyrics.AutoRomanization = true
	first.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	first.syncRomanizer()
	if first.romanizer.ready() == nil {
		t.Fatal("no engine on the first run")
	}
	if !first.romanizer.pinned {
		t.Fatal("the run that writes the cache should report a pinned dictionary")
	}
	first.romanizer.shutdown()

	second := newPlayerService()
	second.appSettings.Playback.Lyrics.AutoRomanization = true
	second.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	second.syncRomanizer()
	if second.romanizer.ready() == nil {
		t.Fatal("no engine on the second run")
	}
	if second.romanizer.pinned {
		t.Fatal("a run reading the cache should own its dictionary")
	}

	loaded := heapMB()
	if loaded < 55 {
		t.Fatalf("the dictionary does not appear loaded: heap=%d MiB", loaded)
	}

	second.appSettings.Playback.Lyrics.AutoRomanization = false
	second.syncRomanizer()
	second.romanizer.releaseAfter(time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		second.romanizer.mu.Lock()
		gone := second.romanizer.engine == nil
		second.romanizer.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := heapMB(); n > loaded-40 {
		t.Fatalf("the dictionary was not returned to the heap: %d MiB -> %d MiB", loaded, n)
	}
}
