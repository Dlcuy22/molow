package romanize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipa.dict")

	if CacheValid(path) {
		t.Fatal("a cache that does not exist reads as valid")
	}
	if err := WriteCache(path); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if CacheValid(path) {
		t.Fatal("an unstamped cache reads as valid")
	}
	if err := RecordCacheStamp(path); err != nil {
		t.Fatalf("RecordCacheStamp: %v", err)
	}
	if !CacheValid(path) {
		t.Fatal("a stamped cache does not read as valid")
	}

	e, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer e.Close()
	if got := e.Romanize("涙はそっと", nil).Line; got != "namidawasotto" {
		t.Fatalf("cache converted to %q", got)
	}

	if err := os.WriteFile(CacheStampPath(path), []byte("some-older-build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if CacheValid(path) {
		t.Fatal("a stale stamp reads as valid")
	}
}

func TestClosedEngineConvertsNothing(t *testing.T) {
	e := New()
	e.Close()

	res := e.Romanize("涙はそっと", nil)
	if res.Line != "" || res.OK {
		t.Fatalf("a closed engine returned %+v", res)
	}
}

func TestConcurrentRomanizeAndClose(t *testing.T) {
	e := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			_ = e.Romanize("涙はそっと枯れていく", nil)
		}
	}()
	e.Close()
	<-done
}
