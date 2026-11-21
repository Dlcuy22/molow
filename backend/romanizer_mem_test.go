package backend

import (
	"testing"
	"time"
)

func TestRomanizerSecondRunMapsOffHeapAndUnloads(t *testing.T) {
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

	before := heapMB()

	second := newPlayerService()
	second.appSettings.Playback.Lyrics.AutoRomanization = true
	second.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	second.syncRomanizer()
	eng := second.romanizer.ready()
	if eng == nil {
		t.Fatal("no engine on the second run")
	}
	if second.romanizer.pinned {
		t.Fatal("a run reading the cache should map ipa.mmap, not pin ipa.Dict")
	}
	if got := eng.Romanize("涙はそっと", nil).Line; got != "namidawasotto" {
		t.Fatalf("mapped engine converted to %q", got)
	}

	if delta := heapMB() - before; delta > 5 {
		t.Fatalf("mmap open added %d MiB to the Go heap, want <= 5 MiB", delta)
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
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the mapped engine was not dropped after the idle window")
}
