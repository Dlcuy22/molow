package backend

import (
	"testing"
)

func TestStartupWithSettingOnLoadsFromCache(t *testing.T) {
	isolateConfig(t)
	isolateCache(t)

	warm := newPlayerService()
	warm.appSettings.Playback.Lyrics.AutoRomanization = true
	warm.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	warm.syncRomanizer()
	if warm.romanizer.ready() == nil {
		t.Fatal("no engine on the cache writer")
	}
	warm.romanizer.shutdown()

	svc := newPlayerService()
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP
	svc.syncRomanizer()

	e := svc.romanizer.ready()
	if e == nil {
		t.Fatal("the startup path produced no engine from the cache")
	}
	if got := e.Romanize("僕は歌う", nil).Line; got != "bokuwautau" {
		t.Fatalf("cached engine converted to %q", got)
	}
	if svc.romanizer.pinned {
		t.Fatal("a run reading an existing cache should not be pinned")
	}
}
