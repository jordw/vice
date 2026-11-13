// speech/stt/numbers.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"slices"
	"strconv"
	"sync"
)

// This file reads spoken numbers. Reading happens in two steps. The first
// is independent of what the number means: consecutive transcript words
// are read as digit strings, where a word that only sounds like a number
// word counts for less than one that is a number word. The second step
// belongs to each quantity (quantities.go): it knows which values the
// controller can mean and maps a digit string to the nearest of them.

// atomKind classifies the pieces that spoken numbers are made of.
type atomKind uint8

const (
	atomDigit    atomKind = iota // "seven", "7"
	atomTeen                     // "ten" through "nineteen"
	atomTens                     // "twenty" through "ninety"
	atomNumeral                  // several digits written together: "270"
	atomHundred                  // "hundred"
	atomThousand                 // "thousand"
)

// numberAtom is one reading of the transcript at a position as a piece of
// a spoken number.
type numberAtom struct {
	kind     atomKind
	digits   string
	end      int     // position after the words read
	evidence float32 // evidence that the words are this piece
	// guessed is set if the words only resemble the number word.
	guessed bool
}

// numberWord is a word of the number vocabulary.
type numberWord struct {
	vocabWord
	kind   atomKind
	digits string
}

var numberVocabulary = func() []numberWord {
	var words []numberWord
	add := func(kind atomKind, digits string, texts ...string) {
		for _, text := range texts {
			words = append(words, numberWord{vocabWord: makeVocabWord(text), kind: kind, digits: digits})
		}
	}
	add(atomDigit, "0", "zero", "oh")
	for i, name := range digitNames[1:] {
		add(atomDigit, strconv.Itoa(i+1), name)
	}
	add(atomDigit, "9", "niner")
	for i, name := range []string{"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen",
		"seventeen", "eighteen", "nineteen"} {
		add(atomTeen, strconv.Itoa(10+i), name)
	}
	for i, name := range []string{"twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"} {
		add(atomTens, strconv.Itoa(20+10*i), name)
	}
	add(atomHundred, "", "hundred")
	add(atomThousand, "", "thousand")
	return words
}()

// commonHomophones are everyday words that sound exactly like a digit.
// They are usually the everyday word, so reading one as the digit counts
// for less.
var commonHomophones = map[string]float32{"to": 0.8, "for": 0.8}

// atomFloor is the least similarity at which a word is read as a number
// word that was misheard.
const atomFloor = 0.7

// commonHomophoneOnly reports whether every token from pos to end is an
// everyday word that only sounds like a digit.
func commonHomophoneOnly(d *decoding, pos, end int) bool {
	for _, t := range d.tokens[pos:end] {
		if _, ok := commonHomophones[t.text]; !ok {
			return false
		}
	}
	return true
}

func isNumberWord(text string) bool {
	return isDigits(text) || slices.ContainsFunc(numberVocabulary, func(w numberWord) bool { return w.text == text })
}

// atomsAt returns the ways the transcript at pos reads as a piece of a
// number.
func (d *decoding) atomsAt(pos int) []numberAtom {
	if atoms, ok := d.atoms[pos]; ok {
		return atoms
	}
	var atoms []numberAtom
	if pos < len(d.tokens) {
		if tok := d.tokens[pos]; tok.numeral {
			kind := atomNumeral
			switch {
			case len(tok.text) == 1:
				kind = atomDigit
			case len(tok.text) == 2 && tok.text[0] == '1':
				kind = atomTeen
			case len(tok.text) == 2 && tok.text[1] == '0':
				kind = atomTens
			}
			atoms = append(atoms, numberAtom{kind: kind, digits: tok.text, end: pos + 1, evidence: tok.sounds.weight()})
			// The recognizer writes the word after a digit as a numeral:
			// "9 or 1000" for "niner thousand".
			switch tok.text {
			case "100":
				atoms = append(atoms, numberAtom{kind: atomHundred, end: pos + 1, evidence: tok.sounds.weight()})
			case "1000":
				atoms = append(atoms, numberAtom{kind: atomThousand, end: pos + 1, evidence: tok.sounds.weight()})
			}
		} else {
			// One word, or two that together sound like one number word
			// ("nine or" for "niner", "seven e" for "seventy").
			for span := 1; span <= 2 && pos+span <= len(d.tokens) && !hesitations[tok.text]; span++ {
				// A second word is part of the number word only if it is
				// not a number itself.
				if span == 2 && isNumberWord(d.tokens[pos+1].text) {
					break
				}
				var best numberWord
				var bestQuality float32
				bestListed := false
				for _, w := range numberVocabulary {
					q := d.spanSimilarity(pos, span, w.vocabWord)
					listed := confusionQuality(d.spanText(pos, pos+span), w.text) > 0
					// A word of the phraseology is that word, and not a
					// number that was misheard, unless the two sound the same.
					if span == 1 && d.phraseology.vocabulary[tok.text] && q < homophoneQuality && !listed {
						continue
					}
					// A known hearing of the recognizer outranks a resemblance.
					if listed && !bestListed || listed == bestListed && q > bestQuality {
						best, bestQuality, bestListed = w, q, listed
					}
				}
				if span == 1 {
					if prior, ok := commonHomophones[tok.text]; ok {
						bestQuality = min(bestQuality, prior)
					}
				}
				if bestQuality >= atomFloor {
					atoms = append(atoms, numberAtom{kind: best.kind, digits: best.digits, end: pos + span,
						evidence: best.weight * gain(bestQuality), guessed: bestQuality < homophoneQuality})
				}
				if bestQuality == 1 {
					break
				}
			}
			// One word that sounds like two digits said together:
			// "tootoo".
			if len(atoms) == 0 && !hesitations[tok.text] && !d.phraseology.vocabulary[tok.text] {
				if pair, ok := digitPair(tok); ok {
					atoms = append(atoms, numberAtom{kind: atomNumeral, digits: pair.digits, end: pos + 1,
						evidence: pair.weight * gain(homophoneQuality)})
				}
			}
		}
	}
	d.atoms[pos] = atoms
	return atoms
}

// digitPairs are the pairs of digits as they sound when said together.
var digitPairs = sync.OnceValue(func() []numberWord {
	var pairs []numberWord
	for _, a := range numberVocabulary {
		for _, b := range numberVocabulary {
			if a.kind == atomDigit && b.kind == atomDigit {
				pairs = append(pairs, numberWord{vocabWord: makeVocabWord(a.text + " " + b.text), kind: atomNumeral,
					digits: a.digits + b.digits})
			}
		}
	}
	return pairs
})

// digitPair returns the pair of digits that a token sounds exactly like,
// if there is one.
func digitPair(tok token) (numberWord, bool) {
	for _, pair := range digitPairs() {
		if slices.Equal(tok.sounds, pair.sounds) {
			return pair, true
		}
	}
	return numberWord{}, false
}

// unitAt returns the evidence that the words at pos are the given unit
// word ("thousand", "hundred"), and the position after them.
func (d *decoding) unitAt(pos int, kind atomKind) (end int, evidence float32, ok bool) {
	for _, a := range d.atomsAt(pos) {
		if a.kind == kind {
			return a.end, a.evidence, true
		}
	}
	return 0, 0, false
}

// numberReading is one reading of consecutive transcript words as a
// string of digits. Leading zeros are kept: they were spoken.
type numberReading struct {
	digits   string
	end      int // position after the last word read
	evidence float32
	last     atomKind
	atoms    int
	// groups holds the digits of each number word that was said: "one two
	// twelve" is 1, 2, 12 and "seventy three" is 73. groupEvidence holds
	// the evidence for each.
	groups        []string
	groupEvidence []float32
	// unitEvidence is the evidence for the last digit alone when the last
	// group is a tens word and a digit ("seventy six"), and 0 otherwise.
	unitEvidence float32
}

func (r numberReading) value() int {
	v, _ := strconv.Atoi(r.digits)
	return v
}

var correctionWord = makeVocabWord("correction")

// maxDigits bounds the digit strings that are read.
const maxDigits = 6

// readNumbers returns every reading of the words at pos as a digit
// string: each prefix of the run of number words there, with a sound-alike
// word either read as a digit or left out.
func (d *decoding) readNumbers(pos int) []numberReading {
	if rs, ok := d.readings[pos]; ok {
		return rs
	}
	var rs []numberReading
	var extend func(r numberReading, skipped bool)
	extend = func(r numberReading, skipped bool) {
		if r.atoms > 0 {
			rs = append(rs, r)
		}
		if len(r.digits) >= maxDigits {
			return
		}
		for _, a := range d.atomsAt(r.end) {
			next := numberReading{end: a.end, evidence: r.evidence + a.evidence, last: a.kind, atoms: r.atoms + 1}
			switch {
			case a.kind == atomThousand:
				continue
			case a.kind == atomHundred:
				// "two hundred" is 200.
				if r.atoms != 1 || len(r.digits) != 1 || r.digits == "0" {
					continue
				}
				next.digits = r.digits + "00"
				next.groups = []string{next.digits}
				next.groupEvidence = []float32{next.evidence}
			case a.kind == atomDigit && r.last == atomTens && r.atoms > 0 && a.digits != "0":
				// "seventy three" is 73.
				next.digits = r.digits[:len(r.digits)-1] + a.digits
				next.last = atomNumeral
				n := len(r.groups) - 1
				next.groups = append(slices.Clone(r.groups[:n]), next.digits[len(next.digits)-2:])
				next.groupEvidence = append(slices.Clone(r.groupEvidence[:n]), r.groupEvidence[n]+a.evidence)
				next.unitEvidence = a.evidence
			default:
				next.digits = r.digits + a.digits
				next.groups = append(slices.Clone(r.groups), a.digits)
				next.groupEvidence = append(slices.Clone(r.groupEvidence), a.evidence)
			}
			extend(next, skipped)
		}
		// A word between the digits that is not a number word: a hesitation
		// costs nothing, and one unintelligible word is tolerated at a cost
		// once a number is certainly being said.
		if r.atoms > 0 && r.end < len(d.tokens) && len(d.atomsAt(r.end+1)) > 0 && !d.tokens[r.end].numeral {
			tok := d.tokens[r.end]
			next := r
			next.end++
			if isStopword(tok.text) {
				extend(next, skipped)
			} else if !skipped && d.isPlainWord(r.end) && !commonHomophoneOnly(d, pos, r.end) {
				next.evidence -= gapCost * tok.sounds.weight()
				extend(next, true)
			}
		}
	}
	extend(numberReading{end: pos}, false)

	// "Two seven zero, correction, two niner zero" is the second number.
	for _, r := range slices.Clone(rs) {
		if d.text(r.end) != "correction" {
			continue
		}
		for _, corrected := range d.readNumbers(r.end + 1) {
			// The first number was heard too, so it stays explained.
			corrected.evidence += r.evidence + correctionWord.weight
			rs = append(rs, corrected)
		}
	}

	// Keep the best-supported reading for each digit string and end.
	slices.SortStableFunc(rs, func(a, b numberReading) int {
		if a.end != b.end {
			return a.end - b.end
		}
		if a.digits != b.digits {
			if a.digits < b.digits {
				return -1
			}
			return 1
		}
		if a.evidence > b.evidence {
			return -1
		} else if a.evidence < b.evidence {
			return 1
		}
		return 0
	})
	rs = slices.CompactFunc(rs, func(a, b numberReading) bool { return a.end == b.end && a.digits == b.digits })

	d.readings[pos] = rs
	return rs
}

// quantity is what a kind of quantity makes of a digit string: the value
// it stands for, and how much of the evidence for the digits carries over. A
// string that is the value as the controller would say it carries all of
// it; one that must be repaired to be a valid value carries less.
type quantity struct {
	value   int
	quality float32
}

// Qualities of the repairs that turn a digit string into a valid value.
const (
	qualityExact         = 1.0
	qualityStutter       = 0.95 // a digit was said twice
	qualityRestored      = 0.90 // a digit the recognizer dropped is put back
	qualityDroppedFirst  = 0.85 // a leading digit is ignored
	qualityDroppedLast   = 0.80 // a trailing digit is ignored
	qualityRounded       = 0.80 // the value is moved to the nearest one that is used
	qualityMisheardDigit = 0.75 // a digit is replaced
	qualityUnlikely      = 0.50 // the value is valid but is not said this way
)

// readQuantity reads the words at pos as a number and interprets each
// reading with interpret. It returns one match per end position: the
// interpretation with the most evidence.
func (d *decoding) readQuantity(pos int, interpret func(r numberReading) (quantity, bool)) []match {
	var ms []match
	for _, r := range d.readNumbers(pos) {
		if in, ok := interpret(r); ok {
			ms = append(ms, match{end: r.end, evidence: r.evidence * in.quality, values: []any{in.value}})
		}
	}
	return ms
}
