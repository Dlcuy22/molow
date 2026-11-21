package romanize

import "unicode/utf8"

type nodeClass uint8

const (
	classDummy nodeClass = iota
	classKnown
	classUnknown
)

const (
	bosEosID                 = -1
	maximumCost              = 1<<31 - 1
	maximumUnknownWordLength = 1024
)

type latticeNode struct {
	id      int32
	start   int32
	class   nodeClass
	left    int16
	right   int16
	weight  int16
	cost    int32
	prev    int32
	surface string
}

type token struct {
	surface string
	reading string
}

// tokenize runs Normal-mode IPADIC Viterbi segmentation over d.
// Returned tokens hold Go-owned reading strings so no pointers into an mmap
// view escape past the caller's read lock.
func (d *mmapDict) tokenize(inp string) []token {
	rc := utf8.RuneCountInString(inp)
	list := make([][]int32, rc+2)
	slab := make([]latticeNode, 0, rc*4+4)

	addNode := func(pos, id, start int, class nodeClass, surface string) {
		var m morph
		switch class {
		case classKnown:
			if id < 0 || id >= len(d.morphs) {
				return
			}
			m = d.morphs[id]
		case classUnknown:
			if id < 0 || id >= len(d.unkMorphs) {
				return
			}
			m = d.unkMorphs[id]
		}
		idx := int32(len(slab))
		slab = append(slab, latticeNode{
			id:      int32(id),
			start:   int32(start),
			class:   class,
			left:    m.LeftID,
			right:   m.RightID,
			weight:  m.Weight,
			prev:    -1,
			surface: surface,
		})
		p := pos + utf8.RuneCountInString(surface)
		list[p] = append(list[p], idx)
	}

	addNode(0, bosEosID, 0, classDummy, "")
	addNode(rc+1, bosEosID, rc, classDummy, "")

	runePos := -1
	for pos, ch := range inp {
		runePos++
		anyMatches := false

		d.commonPrefixSearch(inp[pos:], func(id, byteLen int) {
			addNode(runePos, id, runePos, classKnown, inp[pos:pos+byteLen])
			anyMatches = true
		})

		class := d.charCategory(ch)
		if int(class) >= len(d.invoke) {
			continue
		}
		if !anyMatches || d.invoke[class] != 0 {
			var endPos int
			if ch != utf8.RuneError {
				endPos = pos + utf8.RuneLen(ch)
			} else {
				endPos = pos + 1
			}
			unkWordLen := 1
			if d.group[class] != 0 {
				for i, w, size := endPos, 0, len(inp); i < size; i += w {
					var c rune
					c, w = utf8.DecodeRuneInString(inp[i:])
					if d.charCategory(c) != class {
						break
					}
					endPos += w
					unkWordLen++
					if unkWordLen >= maximumUnknownWordLength {
						break
					}
				}
			}

			prev := pos
			if c, size := utf8.DecodeLastRuneInString(inp[pos:endPos]); c != utf8.RuneError {
				prev = endPos - size
			}
			id := int(d.unkIdx[class])
			dup := int(d.unkDup[class])
			for x := range dup + 1 {
				if pos < prev {
					addNode(runePos, id+x, runePos, classUnknown, inp[pos:prev])
				}
				addNode(runePos, id+x, runePos, classUnknown, inp[pos:endPos])
			}
		}
	}

	for i := 1; i < len(list); i++ {
		for _, tIdx := range list[i] {
			target := &slab[tIdx]
			prevList := list[target.start]
			if len(prevList) == 0 {
				target.cost = maximumCost
				continue
			}
			for j, nIdx := range prevList {
				n := &slab[nIdx]
				c := d.connAt(int(n.right), int(target.left))
				totalCost := int64(c) + int64(target.weight) + int64(n.cost)
				if totalCost > maximumCost {
					totalCost = maximumCost
				}
				if j == 0 || int32(totalCost) < target.cost {
					target.cost = int32(totalCost)
					target.prev = nIdx
				}
			}
		}
	}

	var rev []int32
	for p := list[len(list)-1][0]; p >= 0; p = slab[p].prev {
		if slab[p].id != bosEosID {
			rev = append(rev, p)
		}
	}

	tokens := make([]token, len(rev))
	for i := range rev {
		n := &slab[rev[len(rev)-1-i]]
		tokens[i] = token{
			surface: n.surface,
			reading: d.readingOf(n.class, int(n.id), n.surface),
		}
	}

	return tokens
}
