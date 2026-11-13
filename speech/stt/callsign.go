// speech/stt/callsign.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"cmp"
	"slices"
	"strings"

	"github.com/mmp/vice/sim"
)

// This file finds which aircraft a transmission addresses. A callsign is
// spoken as a name and an identifier: "American fifty nine thirty six",
// "November four eight two Juliett Yankee", "Skyhawk two Juliett Yankee".
// The name is matched by sound, like any other word. The identifier is a
// string of digits and letters that the controller reads out, so it is
// matched as a string: the recognizer drops, repeats and replaces
// characters, and controllers shorten identifiers to their last
// characters.

// addressee is one spoken form by which an aircraft on the frequency can
// be addressed.
type addressee struct {
	key      string // the form as spoken; the key of the aircraft map
	aircraft Aircraft
	names    []vocabWord // the name, and the first word of a longer name
	ident    string      // the characters spoken after the name
	weight   string      // "heavy" or "super" when the form ends with it
}

func makeAddressee(key string, ac Aircraft) addressee {
	a := addressee{key: key, aircraft: ac}
	words := strings.Fields(strings.ToLower(key))
	if n := len(words); n > 0 && (words[n-1] == "heavy" || words[n-1] == "super") {
		a.weight = words[n-1]
		words = words[:n-1]
	}

	callsign := strings.TrimSuffix(ac.Callsign, "/T")
	var name []string
	if ac.AddressingForm == sim.AddressingFormTypeTrailing3 {
		// The aircraft type, then the last three characters of the
		// registration.
		a.ident = strings.TrimPrefix(callsign, "N")
		a.ident = a.ident[max(0, len(a.ident)-3):]
		name = words[:max(0, len(words)-len(a.ident))]
	} else {
		// The name runs up to the first number.
		i := slices.IndexFunc(words, func(w string) bool {
			first, _, _ := strings.Cut(w, "-")
			return w[0] >= '0' && w[0] <= '9' || isNumberWord(first)
		})
		if i < 0 {
			i = len(words)
		}
		name = words[:i]
		a.ident = strings.TrimLeft(callsign, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	}
	if len(name) > 0 {
		a.names = append(a.names, makeVocabWord(strings.Join(name, " ")))
		if len(name) > 1 {
			a.names = append(a.names, makeVocabWord(name[0]))
		}
	}
	return a
}

// makeAddressees returns the addressees for an aircraft map, in a fixed
// order.
func makeAddressees(aircraft map[string]Aircraft) []addressee {
	keys := make([]string, 0, len(aircraft))
	for key := range aircraft {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	addressees := make([]addressee, len(keys))
	for i, key := range keys {
		addressees[i] = makeAddressee(key, aircraft[key])
	}
	return addressees
}

// callsignMatch is a reading of the start of a transmission as the
// callsign of one aircraft.
type callsignMatch struct {
	addressee *addressee
	start     int     // tokens before the callsign that it does not explain
	end       int     // position after the callsign
	evidence  float32 // evidence for the reading, in the units of the grammar
	name      float32 // how closely the spoken name matches, 0 if it was not said
	ident     float32 // how closely the spoken identifier matches, 0 if it was not said
	// kept is the share of the identifier's characters that were heard in
	// order. It separates readings that the evidence does not.
	kept float32
}

const (
	// maxCallsignStart is how many words may precede the callsign.
	maxCallsignStart = 4
	// callsignStartCost is the evidence lost for each of them.
	callsignStartCost = 1.0
	// partialNameQuality scales the match of the first word of a longer
	// name ("Blue" for "Blue Streak").
	partialNameQuality = 0.9
	// nameHeardQuality is how closely words must match a name to count as
	// the name having been said.
	nameHeardQuality = 0.6
	// nameOnlyQuality is how closely a name must match for the callsign to
	// be recognized without its identifier.
	nameOnlyQuality = 0.8
	// certainCallsign is the sum of name and identifier quality at which
	// the decoder is sure enough of the addressee to answer for it when
	// nothing else in the transmission was understood.
	certainCallsign = 1.6
)

// certain reports whether the match identifies the aircraft beyond
// reasonable doubt: its name and its identifier were both heard, or an
// identifier of three characters or more was heard exactly after no more
// than one word that was not understood.
func (m callsignMatch) certain() bool {
	if m.start == 0 && m.name+m.ident >= certainCallsign {
		return true
	}
	return m.start <= 1 && m.ident == 1 && len(m.addressee.ident) >= 3
}

// sameAircraft reports whether two addressees are forms of address for
// one aircraft.
func sameAircraft(a, b addressee) bool {
	return strings.TrimSuffix(a.aircraft.Callsign, "/T") == strings.TrimSuffix(b.aircraft.Callsign, "/T")
}

// identContinues reports whether the word at pos is a number, so that an
// identifier read up to pos stopped short of what was said.
func (d *decoding) identContinues(pos int) bool {
	return pos < len(d.tokens) && isNumberWord(d.tokens[pos].text)
}

// matchCallsigns returns the readings of the start of the transmission as
// a callsign, best first, with at most one reading for each aircraft and
// end position.
func (d *decoding) matchCallsigns(addressees []addressee) []callsignMatch {
	var ms []callsignMatch
	for i := range addressees {
		a := &addressees[i]
		// uniqueTail reports whether a is the only aircraft whose
		// identifier ends with the characters.
		uniqueTail := func(chars string) bool {
			return !slices.ContainsFunc(addressees, func(o addressee) bool {
				return !sameAircraft(o, *a) && strings.HasSuffix(o.ident, chars)
			})
		}
		sameName := func(o addressee) bool {
			return len(o.names) > 0 && len(a.names) > 0 && o.names[0].text == a.names[0].text
		}
		uniqueName := func() bool {
			return !slices.ContainsFunc(addressees, func(o addressee) bool { return !sameAircraft(o, *a) && sameName(o) })
		}
		// uniqueStart reports whether a is the only aircraft with its name
		// whose identifier starts with the characters.
		uniqueStart := func(chars string) bool {
			return !slices.ContainsFunc(addressees, func(o addressee) bool {
				return !sameAircraft(o, *a) && sameName(o) && strings.HasPrefix(o.ident, chars)
			})
		}

		for start := 0; start <= maxCallsignStart && start < len(d.tokens); start++ {
			startCost := callsignStartCost * float32(start)

			// The identifier with no name before it. An aircraft type is
			// what makes the type form of a callsign, so that form is not
			// read without it.
			for _, r := range d.readIdents(start) {
				if a.aircraft.AddressingForm == sim.AddressingFormTypeTrailing3 {
					break
				}
				// An identifier starts where its digits start.
				if start > 0 && isNumberWord(d.tokens[start-1].text) {
					break
				}
				// One character with no name is too little to go on after
				// the first word.
				if start > 0 && len(r.chars) < 2 {
					continue
				}
				if q := identQualityAlone(r, a.ident, uniqueTail); q > 0 && !r.altered {
					ms = append(ms, d.finishCallsign(callsignMatch{addressee: a, start: start, end: r.end,
						evidence: r.evidence*q - startCost, ident: q}))
				}
			}

			// The name, then the identifier.
			for span := 1; span <= maxSpan; span++ {
				var nameQuality, nameWeight float32
				for j, name := range a.names {
					q := d.spanSimilarity(start, span, name)
					if start == 0 && span == 1 {
						// The first sounds of a transmission are often cut
						// off.
						q = max(q, clippedSimilarity(d.tokens[0].sounds, name))
					}
					if j > 0 {
						q *= partialNameQuality
					}
					if q > nameQuality {
						nameQuality, nameWeight = q, name.weight
					}
				}
				if nameQuality < nameHeardQuality {
					continue
				}
				nameEvidence := nameWeight*gain(nameQuality) - startCost

				// The name alone identifies the aircraft only if no other
				// aircraft on the frequency has it, and only if no number
				// follows it: a number that is not this aircraft's belongs
				// to an aircraft that is not on the frequency.
				if nameQuality >= nameOnlyQuality && uniqueName() &&
					(start+span >= len(d.tokens) || len(d.readIdents(start+span)) == 0) {
					ms = append(ms, d.finishCallsign(callsignMatch{addressee: a, start: start, end: start + span,
						evidence: nameEvidence, name: nameQuality}))
				}

				// A word or two between the name and the identifier may be
				// unintelligible.
				pos := start + span
				var gap float32
				for skipped := 0; skipped <= maxGap && pos < len(d.tokens); skipped++ {
					for _, r := range d.readIdents(pos) {
						q := identQuality(r.chars, a.ident, uniqueName())
						if q == 0 && len(r.chars) < len(a.ident) && strings.HasPrefix(a.ident, r.chars) && uniqueStart(r.chars) &&
							!d.identContinues(r.end) {
							// The start of the identifier, with the rest of it
							// not understood.
							q = identFloor
						}
						if r.altered {
							q *= teenTensQuality
						}
						// One character is not the identifier shortened if
						// more of a number follows it.
						if len(r.chars) == 1 && d.identContinues(r.end) {
							q = 0
						}
						if q > 0 {
							kept := float32(commonSubsequence(r.chars, a.ident)) / float32(max(len(r.chars), len(a.ident)))
							ms = append(ms, d.finishCallsign(callsignMatch{addressee: a, start: start, end: r.end,
								evidence: nameEvidence - gap + r.evidence*q, name: nameQuality, ident: q, kept: kept}))
						}
					}
					// A number word starts the identifier. A word that only
					// resembles one may be passed over.
					if isNumberWord(d.tokens[pos].text) {
						break
					}
					gap += gapCost * d.tokens[pos].sounds.weight()
					pos++
				}
			}
		}
	}

	slices.SortStableFunc(ms, func(a, b callsignMatch) int {
		return cmp.Or(
			cmp.Compare(b.evidence, a.evidence),
			cmp.Compare(b.kept, a.kept),
			cmp.Compare(a.addressee.aircraft.AddressingForm, b.addressee.aircraft.AddressingForm),
			cmp.Compare(a.addressee.aircraft.Callsign, b.addressee.aircraft.Callsign))
	})
	// Keep the best reading for each aircraft and end position.
	type seenKey struct {
		callsign string
		end      int
	}
	seen := make(map[seenKey]bool)
	return slices.DeleteFunc(ms, func(m callsignMatch) bool {
		key := seenKey{m.addressee.aircraft.Callsign, m.end}
		if seen[key] {
			return true
		}
		seen[key] = true
		return false
	})
}

// clippedSimilarity returns how closely a word sounds like the end of a
// name: the name with its first sounds cut off ("blue" for "JetBlue").
func clippedSimilarity(heard pronunciation, name vocabWord) float32 {
	if len(heard) < 2 || len(heard) >= len(name.sounds) || heard.weight() < truncationShare*name.weight {
		return 0
	}
	return truncationQuality * soundSimilarity(heard, name.sounds[len(name.sounds)-len(heard):])
}

// finishCallsign accounts for the weight class word after a callsign:
// "heavy" or "super", possibly after one word that was not understood. It
// is evidence for an aircraft of that class and against any other.
func (d *decoding) finishCallsign(m callsignMatch) callsignMatch {
	for pos := m.end; pos <= m.end+1 && pos < len(d.tokens); pos++ {
		tok := d.tokens[pos]
		if tok.text == "heavy" || tok.text == "super" {
			if tok.text == m.addressee.weight {
				m.evidence += tok.sounds.weight()
			} else {
				m.evidence -= tok.sounds.weight()
			}
			m.end = pos + 1
			break
		}
		if tok.numeral || len(d.atomsAt(pos)) > 0 {
			break
		}
	}
	return m
}

const (
	// identFloor is the least quality at which a spoken identifier is taken
	// to be an aircraft's.
	identFloor = 0.55
	// namelessIdentFloor is the same when the aircraft's name was not heard
	// before the identifier.
	namelessIdentFloor = 0.65
	// guessedIdentFloor is the same when the first word of the identifier
	// only resembles a number.
	guessedIdentFloor = 0.75
)

// identQuality returns how well the characters that were heard match an
// aircraft's identifier, from 0 (not a match) to 1, when the aircraft's
// name was heard before them. onlyName says that no other aircraft on the
// frequency has that name: the name then says which aircraft is meant,
// and the identifier needs only to resemble what was heard.
func identQuality(heard, ident string, onlyName bool) float32 {
	if q := identShape(heard, ident); q > 0 {
		return q
	}
	if q := identResemblance(heard, ident, onlyName); q >= identFloor {
		return q
	}
	return 0
}

// identShape returns the quality of a match in which the identifier is
// whole: said in full, shortened to its last characters as controllers
// do, or with something before it run into it.
func identShape(heard, ident string) float32 {
	switch {
	case heard == ident:
		return 1
	case strings.HasSuffix(ident, heard):
		// The identifier shortened to its last characters.
		if len(heard) < 2 {
			return 0.6
		}
		return 0.85
	case strings.HasSuffix(heard, ident) && 2*len(ident) >= len(heard):
		// Something before the identifier ran into it. Only the
		// identifier's share of what was heard counts.
		return 0.9 * float32(len(ident)) / float32(len(heard))
	}
	return 0
}

// identResemblance returns how closely the characters that were heard
// resemble the identifier when a character or two was dropped, repeated,
// replaced or swapped. Letters are distinct enough that one the
// identifier lacks rules it out. moved allows for a character that was
// heard out of place ("six five four" for 648): the measure is then the
// share of the characters that were kept in order.
func identResemblance(heard, ident string, moved bool) float32 {
	dist := editDistance(heard, ident)
	if dist > 1 && foreignLetter(heard, ident) {
		return 0
	}
	longer := max(len(heard), len(ident))
	q := 1 - float32(dist)/float32(longer+1)
	if moved {
		q = max(q, float32(commonSubsequence(heard, ident))/float32(longer))
	}
	return q
}

// commonSubsequence returns the length of the longest sequence of
// characters that appears in order in both strings.
func commonSubsequence(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else {
				cur[j] = max(prev[j], cur[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// foreignLetter reports whether heard holds a letter that ident lacks.
func foreignLetter(heard, ident string) bool {
	return strings.ContainsFunc(heard, func(r rune) bool {
		return r >= 'A' && r <= 'Z' && !strings.ContainsRune(ident, r)
	})
}

// identQualityAlone is identQuality for an identifier that was heard
// without the aircraft's name. With less to go on, a short or shortened
// identifier counts only when it is unambiguous, and a resemblance must be
// closer.
func identQualityAlone(r identReading, ident string, uniqueTail func(chars string) bool) float32 {
	heard := r.chars
	switch {
	case heard == ident:
		return 1
	case strings.HasSuffix(ident, heard):
		if len(heard) >= 2 && uniqueTail(heard) {
			return 0.7
		}
		return 0
	case len(heard) < 3 || len(ident) < 2 || foreignLetter(heard, ident):
		return 0
	}
	if q := identShape(heard, ident); q > 0 {
		return q
	}
	floor := float32(namelessIdentFloor)
	if r.guessed {
		floor = guessedIdentFloor
	}
	if q := identResemblance(heard, ident, false); q >= floor {
		return q
	}
	return 0
}

// editDistance returns the number of single-character insertions,
// deletions and substitutions, and swaps of adjacent characters, that
// turn a into b.
func editDistance(a, b string) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}

// identReading is one reading of consecutive transcript words as the
// digits and letters of an identifier.
type identReading struct {
	chars    string
	end      int
	evidence float32
	// altered is set if a number word was read as the word it is confused
	// with: "fourteen" as forty.
	altered bool
	// guessed is set if the first word only resembles a number word: it
	// is where the aircraft's name belongs, and may be that name.
	guessed bool
}

// maxIdentChars bounds the identifiers that are read.
const maxIdentChars = 7

// readIdents returns the readings of the words at pos as an identifier:
// digits as in any number, and letters said as letters or in the
// radiotelephony alphabet.
func (d *decoding) readIdents(pos int) []identReading {
	if rs, ok := d.idents[pos]; ok {
		return rs
	}
	type state struct {
		identReading
		last    atomKind
		skipped bool // a word that was not understood was passed over
	}
	var rs []identReading
	var extend func(s state)
	extend = func(s state) {
		if s.chars != "" {
			rs = append(rs, s.identReading)
		}
		if len(s.chars) >= maxIdentChars {
			return
		}
		for _, a := range d.atomsAt(s.end) {
			next := state{identReading{end: a.end, evidence: s.evidence + a.evidence, altered: s.altered,
				guessed: s.guessed || s.chars == "" && a.guessed}, a.kind, s.skipped}
			switch {
			case a.kind == atomHundred || a.kind == atomThousand:
				if s.chars == "" {
					continue
				}
				// "Hundred" where a number belongs is a number that was not
				// understood.
				if a.kind == atomHundred && !s.skipped {
					extend(state{identReading{chars: s.chars, end: a.end, evidence: s.evidence, altered: s.altered,
						guessed: s.guessed}, s.last, true})
				}
				// "eleven hundred" is 1100.
				next.chars = s.chars + map[atomKind]string{atomHundred: "00", atomThousand: "000"}[a.kind]
			case a.kind == atomDigit && s.last == atomTens && s.chars != "" && a.digits != "0":
				next.chars = s.chars[:len(s.chars)-1] + a.digits
				next.last = atomNumeral
			default:
				next.chars = s.chars + a.digits
			}
			extend(next)

			// "Fourteen" and "forty" are heard for each other.
			if confused, ok := teenOrTens(a); ok && !d.tokens[s.end].numeral {
				extend(state{identReading{chars: s.chars + confused.digits, end: a.end,
					evidence: s.evidence + a.evidence, altered: true, guessed: s.guessed || s.chars == "" && a.guessed},
					confused.kind, s.skipped})
			}
		}
		// A number word that is read as a letter ("zero" for "zulu") is
		// not what the recognizer wrote.
		if letter, end, evidence, ok := d.letterAt(s.end); ok {
			extend(state{identReading{chars: s.chars + letter, end: end, evidence: s.evidence + evidence,
				altered: s.altered || isNumberWord(d.text(s.end)), guessed: s.guessed}, atomNumeral, s.skipped})
		}
		// "sixteen and twelve".
		if s.chars != "" && d.text(s.end) == "and" && len(d.atomsAt(s.end+1)) > 0 {
			next := s
			next.end++
			extend(next)
		}
		// One word inside the identifier that was not understood.
		if s.chars != "" && !s.skipped && d.isPlainWord(s.end) && d.identCharAt(s.end+1) {
			next := s
			next.evidence -= gapCost * d.tokens[s.end].sounds.weight()
			next.end++
			next.skipped = true
			extend(next)
		}
	}
	extend(state{identReading: identReading{end: pos}})

	slices.SortStableFunc(rs, func(a, b identReading) int {
		return cmp.Or(cmp.Compare(a.end, b.end), cmp.Compare(a.chars, b.chars), cmp.Compare(b.evidence, a.evidence))
	})
	rs = slices.CompactFunc(rs, func(a, b identReading) bool { return a.end == b.end && a.chars == b.chars })
	d.idents[pos] = rs
	return rs
}

// teenTensQuality scales the match of an identifier in which a number
// word was read as the word it is confused with.
const teenTensQuality = 0.8

// teenOrTens returns the number that the recognizer confuses with a teen
// or a tens word: forty for fourteen, and the reverse.
func teenOrTens(a numberAtom) (numberAtom, bool) {
	switch {
	case a.kind == atomTeen && a.digits >= "13":
		return numberAtom{kind: atomTens, digits: a.digits[1:] + "0"}, true
	case a.kind == atomTens && a.digits >= "30":
		return numberAtom{kind: atomTeen, digits: "1" + a.digits[:1]}, true
	}
	return numberAtom{}, false
}

// identCharAt reports whether a digit or a letter of an identifier starts
// at pos.
func (d *decoding) identCharAt(pos int) bool {
	if pos >= len(d.tokens) {
		return false
	}
	if len(d.atomsAt(pos)) > 0 {
		return true
	}
	_, _, _, ok := d.letterAt(pos)
	return ok
}

// alphabet is the radiotelephony spelling alphabet.
var alphabet = func() []vocabWord {
	var words []vocabWord
	for _, w := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india",
		"juliett", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu"} {
		words = append(words, makeVocabWord(w))
	}
	return words
}()

// spelledLetterQuality is the least similarity at which a word is taken
// for a word of the spelling alphabet.
const spelledLetterQuality = 0.75

// letterAt reads the words at pos as one letter: a word of the spelling
// alphabet, or a letter said by name.
func (d *decoding) letterAt(pos int) (letter string, end int, evidence float32, ok bool) {
	if pos >= len(d.tokens) || d.tokens[pos].numeral {
		return "", 0, 0, false
	}
	var best vocabWord
	var bestQuality float32
	for span := 1; span <= 2; span++ {
		for _, w := range alphabet {
			if q := d.spanSimilarity(pos, span, w); q > bestQuality {
				best, bestQuality, end = w, q, pos+span
			}
		}
	}
	if bestQuality >= spelledLetterQuality {
		return strings.ToUpper(best.text[:1]), end, best.weight * gain(bestQuality), true
	}
	if tok := d.tokens[pos]; len(tok.text) == 1 {
		return strings.ToUpper(tok.text), pos + 1, tok.sounds.weight() * gain(homophoneQuality), true
	}
	return "", 0, 0, false
}
