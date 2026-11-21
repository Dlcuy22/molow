package romanize

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/dlcuy22/molow/internal/lyrics"
)

func TestMmapParityWithEmbedded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipa.mmap")
	if err := WriteCache(path); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}

	mapped, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mapped.Close()

	embedded := New()

	type sample struct {
		text  string
		words []Word
	}
	samples := []sample{
		{text: "涙はそっと枯れていく", words: []Word{{Text: "涙"}, {Text: "は"}, {Text: "そっと"}, {Text: "枯れて"}, {Text: "いく"}}},
		{text: "貴方が褒めてくれた"},
		{text: "雨が降る夜に hello world 123"},
		{text: "ずっと、ずっと…（大丈夫！）"},
		{text: "ッーカタカナとひらがなと漢字"},
		{text: ""},
	}

	fixtures, err := filepath.Glob(filepath.Join("..", "lyrics", "testdata", "romanize", "*.lrc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range fixtures {
		raw, err := os.ReadFile(fp)
		if err != nil {
			t.Fatal(err)
		}
		lines, _, err := lyrics.Parse(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			ws := make([]Word, len(line.Words))
			for i, w := range line.Words {
				ws[i] = Word{Text: w.Text}
			}
			samples = append(samples, sample{text: line.Text, words: ws})
		}
	}

	for _, spacing := range []bool{false, true} {
		opts := Options{Spacing: spacing}
		for _, s := range samples {
			want := embedded.RomanizeWith(s.text, s.words, opts)
			got := mapped.RomanizeWith(s.text, s.words, opts)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("spacing=%v text=%q:\ngot  %+v\nwant %+v", spacing, s.text, got, want)
			}
		}
	}
}

func TestMmapConcurrentRomanizeAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipa.mmap")
	if err := WriteCache(path); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	e, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				res := e.Romanize("涙はそっと枯れていく", nil)
				if res.Line != "" && res.Line != "namidawasottokareteiku" {
					t.Errorf("unexpected result during concurrent close: %q", res.Line)
				}
			}
		}()
	}
	e.Close()
	wg.Wait()

	if res := e.Romanize("涙はそっと", nil); res.Line != "" || res.OK {
		t.Fatalf("closed mmap engine returned %+v", res)
	}
}

func TestMmapRejectsCorruptFiles(t *testing.T) {
	dir := t.TempDir()

	writeCase := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}

		return p
	}

	badMagic := make([]byte, headerSize)
	copy(badMagic, "NOTVALID")

	badVersion := make([]byte, headerSize)
	hdr := fileHeader{Magic: fileMagic, Version: 99}
	_, _ = binary.Encode(badVersion, binary.LittleEndian, hdr)

	outOfBounds := make([]byte, headerSize+16)
	hdrOOB := fileHeader{
		Magic:      fileMagic,
		Version:    formatVersion,
		DaOff:      headerSize,
		DaLen:      100,
		CharCatOff: headerSize,
		CharCatLen: 1,
		ClassLen:   1,
	}
	_, _ = binary.Encode(outOfBounds, binary.LittleEndian, hdrOOB)

	misaligned := make([]byte, headerSize+64)
	hdrAlign := fileHeader{
		Magic:      fileMagic,
		Version:    formatVersion,
		DaOff:      headerSize + 1,
		DaLen:      1,
		CharCatOff: headerSize,
		CharCatLen: 1,
		ClassLen:   1,
	}
	_, _ = binary.Encode(misaligned, binary.LittleEndian, hdrAlign)

	cases := []struct {
		name string
		path string
	}{
		{"empty", writeCase("empty.mmap", nil)},
		{"short", writeCase("short.mmap", []byte("MLWIPA01"))},
		{"bad_magic", writeCase("magic.mmap", badMagic)},
		{"bad_version", writeCase("version.mmap", badVersion)},
		{"out_of_bounds", writeCase("oob.mmap", outOfBounds)},
		{"misaligned", writeCase("align.mmap", misaligned)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Open(tc.path)
			if err == nil {
				e.Close()
				t.Fatalf("Open(%s) succeeded on corrupt file", tc.name)
			}
		})
	}
}
