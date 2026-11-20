package romanize

import (
	"strings"
)

// kanaTable maps hiragana to traditional Hepburn. Long vowels are doubled
// instead of using macrons.
var kanaTable = map[string]string{
	"あ": "a", "い": "i", "う": "u", "え": "e", "お": "o",
	"か": "ka", "き": "ki", "く": "ku", "け": "ke", "こ": "ko",
	"が": "ga", "ぎ": "gi", "ぐ": "gu", "げ": "ge", "ご": "go",
	"さ": "sa", "し": "shi", "す": "su", "せ": "se", "そ": "so",
	"ざ": "za", "じ": "ji", "ず": "zu", "ぜ": "ze", "ぞ": "zo",
	"た": "ta", "ち": "chi", "つ": "tsu", "て": "te", "と": "to",
	"だ": "da", "ぢ": "ji", "づ": "zu", "で": "de", "ど": "do",
	"な": "na", "に": "ni", "ぬ": "nu", "ね": "ne", "の": "no",
	"は": "ha", "ひ": "hi", "ふ": "fu", "へ": "he", "ほ": "ho",
	"ば": "ba", "び": "bi", "ぶ": "bu", "べ": "be", "ぼ": "bo",
	"ぱ": "pa", "ぴ": "pi", "ぷ": "pu", "ぺ": "pe", "ぽ": "po",
	"ま": "ma", "み": "mi", "む": "mu", "め": "me", "も": "mo",
	"や": "ya", "ゆ": "yu", "よ": "yo",
	"ら": "ra", "り": "ri", "る": "ru", "れ": "re", "ろ": "ro",
	"わ": "wa", "ゐ": "i", "ゑ": "e", "を": "o", "ん": "n",
	"ゔ": "vu", "ゎ": "wa", "ゕ": "ka", "ゖ": "ke",
	"ぁ": "a", "ぃ": "i", "ぅ": "u", "ぇ": "e", "ぉ": "o",
	"ゃ": "ya", "ゅ": "yu", "ょ": "yo", "っ": "", "ー": "",

	"きゃ": "kya", "きゅ": "kyu", "きょ": "kyo",
	"ぎゃ": "gya", "ぎゅ": "gyu", "ぎょ": "gyo",
	"しゃ": "sha", "しゅ": "shu", "しょ": "sho", "しぇ": "she",
	"じゃ": "ja", "じゅ": "ju", "じょ": "jo", "じぇ": "je",
	"ちゃ": "cha", "ちゅ": "chu", "ちょ": "cho", "ちぇ": "che",
	"ぢゃ": "ja", "ぢゅ": "ju", "ぢょ": "jo",
	"にゃ": "nya", "にゅ": "nyu", "にょ": "nyo",
	"ひゃ": "hya", "ひゅ": "hyu", "ひょ": "hyo",
	"びゃ": "bya", "びゅ": "byu", "びょ": "byo",
	"ぴゃ": "pya", "ぴゅ": "pyu", "ぴょ": "pyo",
	"みゃ": "mya", "みゅ": "myu", "みょ": "myo",
	"りゃ": "rya", "りゅ": "ryu", "りょ": "ryo",
	"ふぁ": "fa", "ふぃ": "fi", "ふぇ": "fe", "ふぉ": "fo", "ふゅ": "fyu",
	"てぃ": "ti", "でぃ": "di", "とぅ": "tu", "どぅ": "du",
	"うぃ": "wi", "うぇ": "we", "うぉ": "wo",
	"ゔぁ": "va", "ゔぃ": "vi", "ゔぇ": "ve", "ゔぉ": "vo",
	"すぃ": "si", "ずぃ": "zi", "つぁ": "tsa", "つぃ": "tsi", "つぇ": "tse", "つぉ": "tso",
}

// Parts converts consecutive kana readings to Hepburn as one stream so っ and
// ー carry across part boundaries while keeping one output string per part.
func Parts(readings []string) []string {
	if len(readings) == 0 {
		return nil
	}
	var (
		flat   []rune
		partOf []int
	)
	for i, p := range readings {
		for _, r := range p {
			flat = append(flat, katakanaRune(r))
			partOf = append(partOf, i)
		}
	}
	out := make([]strings.Builder, len(readings))
	lastV := ""
	add := func(s string, at int) {
		out[partOf[at]].WriteString(s)
		if s != "" && strings.ContainsAny(s[len(s)-1:], "aeiou") {
			lastV = s[len(s)-1:]
		} else {
			lastV = ""
		}
	}
	for i := 0; i < len(flat); i++ {
		switch flat[i] {
		case 'ー':
			if lastV != "" {
				add(lastV, i)
			}
			continue
		case 'っ':
			if i+1 < len(flat) {
				next, _ := lookup(flat[i+1:])
				if next != "" && !strings.ContainsAny(next[:1], "aeiou") {
					if strings.HasPrefix(next, "ch") {
						add("t", i)
					} else {
						add(next[:1], i)
					}
				}
			}
			continue
		case 'ん':
			add(nasal(flat[i+1:]), i)
			continue
		}
		key, used := lookup(flat[i:])
		if key == "" {
			add(string(flat[i]), i)
			continue
		}
		add(key, i)
		i += used - 1
	}
	res := make([]string, len(readings))
	for i := range out {
		res[i] = out[i].String()
	}

	return res
}

// katakanaRune folds katakana to hiragana, leaving ー and other runes intact.
func katakanaRune(r rune) rune {
	if r >= 0x30A1 && r <= 0x30F6 {
		return r - 0x60
	}
	if r == 0x30FD || r == 0x30FE { // ヽ ヾ
		return r - 0x60
	}

	return r
}

func lookup(r []rune) (string, int) {
	if len(r) >= 2 {
		if v, ok := kanaTable[string(r[:2])]; ok {
			return v, 2
		}
	}
	if v, ok := kanaTable[string(r[:1])]; ok {
		return v, 1
	}

	return "", 0
}

// nasal spells ん as m before b/m/p, n' before vowels or y, and n otherwise.
func nasal(rest []rune) string {
	if len(rest) == 0 {
		return "n"
	}
	next, _ := lookup(rest)
	if next == "" {
		return "n"
	}
	switch next[0] {
	case 'a', 'i', 'u', 'e', 'o', 'y':
		return "n'"
	case 'b', 'm', 'p':
		return "m"
	}

	return "n"
}

func isKana(r rune) bool {
	return (r >= 0x3041 && r <= 0x3096) || (r >= 0x30A1 && r <= 0x30F6) || r == 0x30FC
}
