package trackcache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutGetAndLRUOrder(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 100)
	if err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	a := bytes.Repeat([]byte("a"), 40)
	b := bytes.Repeat([]byte("b"), 40)
	c := bytes.Repeat([]byte("c"), 40)

	if err := s.Put("trackA", a); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	if err := s.Put("trackB", b); err != nil {
		t.Fatal(err)
	}

	// Reading trackA promotes it ahead of trackB and updates its mtime.
	clock = clock.Add(2 * time.Minute)
	rc, size, ok := s.Get("trackA")
	if !ok || size != 40 {
		t.Fatalf("Get(trackA) = (size=%d, ok=%v), want (40, true)", size, ok)
	}
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, a) {
		t.Fatalf("read trackA = (len=%d, err=%v)", len(got), err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}

	// Writing trackC exceeds 100 bytes; trackB (oldest unused) must be evicted.
	clock = clock.Add(2 * time.Minute)
	if err := s.Put("trackC", c); err != nil {
		t.Fatal(err)
	}

	if s.Has("trackB") {
		t.Fatal("trackB should have been evicted as LRU")
	}
	if !s.Has("trackA") || !s.Has("trackC") {
		t.Fatalf("expected trackA and trackC to remain, stats=%+v", s.Stats())
	}
	if st := s.Stats(); st.Count != 2 || st.TotalBytes != 80 {
		t.Fatalf("stats = %+v, want count=2 total=80", st)
	}
}

func TestEvictionSkipsInUseAndRetriesOnClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 80)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Put("old", bytes.Repeat([]byte("1"), 40)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("mid", bytes.Repeat([]byte("2"), 40)); err != nil {
		t.Fatal(err)
	}

	// Pin old while still keeping it at LRU tail by checking Put of new entry
	// after pinning: Get promotes old, so pin mid first then touch old so old
	// stays MRU? No: pin old, then Put mid2 so old is LRU and pinned!
	// Easiest: open old, then Put mid (promotes mid to MRU, old is now LRU & inUse).
	rc, _, ok := s.Get("old")
	if !ok {
		t.Fatal("Get(old) missed")
	}
	if err := s.Put("mid", bytes.Repeat([]byte("2"), 40)); err != nil {
		t.Fatal(err)
	}

	// Putting new pushes total to 120 > 80. old is LRU but inUse, so mid is
	// evicted instead and old survives while open.
	if err := s.Put("new", bytes.Repeat([]byte("3"), 40)); err != nil {
		t.Fatal(err)
	}
	if !s.Has("old") {
		t.Fatal("in-use entry old was evicted while open")
	}
	if s.Has("mid") {
		t.Fatal("unused entry mid should have been evicted")
	}

	// Lower budget to 20 while old (40 bytes) is still open: unused new is
	// evicted immediately, old stays until closed, and Close evicts old right
	// away.
	if err := s.SetMaxBytes(20); err != nil {
		t.Fatal(err)
	}
	if !s.Has("old") {
		t.Fatal("in-use entry old was evicted on SetMaxBytes while open")
	}
	if s.Has("new") {
		t.Fatal("unused entry new should have been evicted on SetMaxBytes")
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Has("old") {
		t.Fatal("old should have been evicted once closed under tight budget")
	}
	if st := s.Stats(); st.Count != 0 || st.TotalBytes != 0 {
		t.Fatalf("stats after Close = %+v, want empty", st)
	}
}

func TestOversizedPutDoesNotWipeExistingEntries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 50)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Put("small", bytes.Repeat([]byte("s"), 30)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("huge", bytes.Repeat([]byte("h"), 80)); err != nil {
		t.Fatal(err)
	}
	if !s.Has("small") {
		t.Fatal("existing entry wiped by oversized Put")
	}
	if s.Has("huge") {
		t.Fatal("oversized entry should not be stored")
	}
}

func TestOpenSweepsTempFilesAndOrdersByMtime(t *testing.T) {
	dir := t.TempDir()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	writeTimed := func(name string, size int, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	writeTimed("oldest.webm", 40, base)
	writeTimed("newest.webm", 40, base.Add(10*time.Minute))
	writeTimed("middle.webm", 40, base.Add(5*time.Minute))
	writeTimed("aborted.tmp1234", 20, base)
	writeTimed("empty.webm", 0, base)

	// Budget 80 forces oldest.webm to be evicted on Open.
	s, err := Open(dir, 80)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "aborted.tmp1234")); !os.IsNotExist(err) {
		t.Fatal("stale temp file was not swept on Open")
	}
	if _, err := os.Stat(filepath.Join(dir, "empty.webm")); !os.IsNotExist(err) {
		t.Fatal("empty webm file was not swept on Open")
	}
	if s.Has("oldest") {
		t.Fatal("oldest.webm should have been evicted on Open")
	}
	if !s.Has("middle") || !s.Has("newest") {
		t.Fatalf("expected middle and newest to survive, stats=%+v", s.Stats())
	}
}

func TestClearAndSetMaxBytesZero(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("a", bytes.Repeat([]byte("a"), 50)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b", bytes.Repeat([]byte("b"), 50)); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Count != 0 || st.TotalBytes != 0 {
		t.Fatalf("after Clear stats = %+v, want empty", st)
	}

	// Budget 0 disables writes.
	if err := s.SetMaxBytes(0); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("c", bytes.Repeat([]byte("c"), 10)); err != nil {
		t.Fatal(err)
	}
	if s.Has("c") {
		t.Fatal("Put stored entry when maxBytes == 0")
	}
}

func TestRejectsUnsafeKeys(t *testing.T) {
	s, err := Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", ".", "..", "../escape", `a\b`, "a/b", "a.b"} {
		if err := s.Put(bad, []byte("data")); err != nil {
			t.Fatalf("Put(%q) err = %v", bad, err)
		}
		if s.Has(bad) {
			t.Fatalf("unsafe key %q was accepted", bad)
		}
		if _, _, ok := s.Get(bad); ok {
			t.Fatalf("unsafe key %q returned hit", bad)
		}
	}
}
