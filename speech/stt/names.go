// speech/stt/names.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/speech"
)

// This file reads names: fixes, airports, procedures and the other
// things a controller refers to by name. The names an aircraft can be
// given come from its context, so each reading is a choice among a small
// set of candidates, made by sound.

// name is one thing that can be named, with the way it is spoken.
type name struct {
	spoken vocabWord
	id     string
	// prior scales the similarity of this name: less than 1 for a name
	// that is less likely to be meant than the others.
	prior float32
}

const (
	// nameFloor is the least similarity at which transcript words are
	// taken to be a name.
	nameFloor = 0.62
	// identifierPrior scales a match against a fix's identifier read as a
	// word, which is how the recognizer sometimes spells what it hears.
	identifierPrior = 0.9
	// approachFixPrior scales the fixes of approaches that the aircraft was
	// not told to expect.
	approachFixPrior = 0.95
)

// readName returns the readings of the words at pos as one of the names:
// for each span of up to maxSpan words, the name that sounds most like
// it.
func (d *decoding) readName(pos int, names []name) []match {
	var ms []match
	for span := 1; span <= maxSpan && pos+span <= len(d.tokens); span++ {
		var best name
		var bestQuality float32
		// The small words of the language are not names that were
		// misheard.
		small := span == 1 && isFunctionWord(d.tokens[pos].text)
		// A word, but not a letter, may be the start of a name. That is
		// the reading of last resort: a word that sounds like a whole name
		// is that name.
		var cut name
		var cutQuality float32
		for _, n := range names {
			q := d.soundsLike(pos, span, n.spoken)
			if small && q < 1 {
				continue
			}
			if q *= n.prior; q > bestQuality {
				best, bestQuality = n, q
			}
			if span == 1 && len(d.tokens[pos].text) > 1 {
				if q := truncationSimilarity(d.tokens[pos].sounds, n.spoken) * n.prior; q > cutQuality {
					cut, cutQuality = n, q
				}
			}
		}
		if bestQuality < nameFloor {
			best, bestQuality = cut, cutQuality
		}
		if bestQuality >= nameFloor {
			ms = append(ms, match{end: pos + span, evidence: best.spoken.weight * gain(bestQuality), values: []any{best.id}})
		}
	}
	return ms
}

const (
	// truncationQuality scales the similarity of a word to the start of a
	// name: the recognizer cuts words short ("age" for "Agers").
	truncationQuality = 0.85
	// truncationShare is the least share of a name's sound that a word
	// must have to be the name cut short.
	truncationShare = 0.3
)

// truncationSimilarity returns how closely a word sounds like the start
// of a name, or 0 if it is too short to be the name cut short.
func truncationSimilarity(heard pronunciation, name vocabWord) float32 {
	if len(heard) < 2 || len(heard) >= len(name.sounds) || heard.weight() < truncationShare*name.weight {
		return 0
	}
	return truncationQuality * soundSimilarity(heard, name.sounds[:len(heard)])
}

// makeNames builds the candidate list for a map from spoken name to
// identifier, in a fixed order.
func makeNames(spoken map[string]string, prior float32) []name {
	var names []name
	for text, id := range spoken {
		names = append(names, name{spoken: makeVocabWord(strings.ToLower(text)), id: id, prior: prior})
	}
	slices.SortFunc(names, func(a, b name) int {
		return cmp.Or(cmp.Compare(a.id, b.id), cmp.Compare(a.spoken.text, b.spoken.text))
	})
	return names
}

// fixNames returns the fixes the aircraft can be sent to: those of its
// route, and those of the approaches it may fly.
func (d *decoding) fixNames() []name {
	if d.fixes != nil {
		return d.fixes
	}
	ac := d.aircraft
	names := makeNames(ac.Fixes, 1)
	known := make(map[string]bool)
	for _, n := range names {
		known[n.id] = true
	}
	for _, id := range slices.Sorted(mapKeys(ac.ApproachFixes)) {
		for _, n := range makeNames(ac.ApproachFixes[id], approachFixPrior) {
			if !known[n.id] {
				known[n.id] = true
				names = append(names, n)
			}
		}
	}
	// The identifier itself, sounded out.
	for id := range known {
		if !slices.ContainsFunc(names, func(n name) bool { return n.spoken.text == strings.ToLower(id) }) {
			names = append(names, name{spoken: makeVocabWord(strings.ToLower(id)), id: id, prior: identifierPrior})
		}
	}
	slices.SortStableFunc(names, func(a, b name) int { return cmp.Compare(b.prior, a.prior) })
	d.fixes = names
	return names
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// readSpelling reads the words at pos as letters of the spelling
// alphabet, after an optional "that's" or "spelled".
func (d *decoding) readSpelling(pos int) (letters string, end int, evidence float32) {
	if t := d.text(pos); t == "thats" || t == "spelled" || t == "spelling" {
		pos++
	}
	for {
		letter, next, e, ok := d.letterAt(pos)
		if !ok || len(d.text(pos)) == 1 {
			return letters, pos, evidence
		}
		letters += letter
		pos, evidence = next, evidence+e
	}
}

// readFix reads the words at pos as a fix where other things can be said
// as well.
func (d *decoding) readFix(pos int) []match { return d.readFixAs(pos, false) }

// readFixAs reads the words at pos as a fix: its name, its identifier
// spelled in the alphabet, or the name followed by the spelling. A number
// word is a name that was misheard only where a fix is expected.
func (d *decoding) readFixAs(pos int, expected bool) []match {
	names := d.fixNames()
	if len(names) == 0 {
		return nil
	}

	// A spelling stands on its own, and it settles what a name before it
	// was: "Deer Park, that's delta papa kilo".
	spelled := func(pos int) (id string, end int, evidence float32, ok bool) {
		id, end, evidence = d.readSpelling(pos)
		ok = len(id) >= 2 && slices.ContainsFunc(names, func(n name) bool { return n.id == id })
		return
	}

	var ms []match
	if letters, end, evidence := d.readSpelling(pos); len(letters) >= 2 {
		// Two letters or more are a spelling, and not a name that was
		// misheard. A spelling that is no identifier is of the fix whose
		// name sounds like the letters: "papa juliett" for "PB Jay".
		if id, end, evidence, ok := spelled(pos); ok {
			return []match{{end: end, evidence: evidence, values: []any{id}}}
		}
		said := makeVocabWord(strings.ToLower(strings.Join(strings.Split(letters, ""), " ")))
		var best name
		var bestQuality float32
		for _, n := range names {
			if q := soundSimilarity(said.sounds, n.spoken.sounds) * n.prior; q > bestQuality {
				best, bestQuality = n, q
			}
		}
		if bestQuality >= nameFloor {
			ms = append(ms, match{end: end, evidence: evidence * bestQuality, values: []any{best.id}})
		}
		return ms
	}

	ms = d.readName(pos, names)
	if !expected && isNumberWord(d.text(pos)) {
		ms = slices.DeleteFunc(ms, func(m match) bool { return m.end == pos+1 })
	}
	for _, m := range slices.Clone(ms) {
		if id, end, evidence, ok := spelled(m.end); ok {
			ms = append(ms, match{end: end, evidence: m.evidence + evidence, values: []any{id}})
		}
	}
	// One word that was not understood, then the spelling.
	if pos < len(d.tokens) && !d.tokens[pos].numeral {
		if id, end, evidence, ok := spelled(pos + 1); ok {
			ms = append(ms, match{end: end, evidence: evidence, values: []any{id}})
		}
	}
	return ms
}

// expectedNameMargin is how much closer a word must be to one name than
// to every other for it to be taken as that name where one is expected.
const expectedNameMargin = 0.1

// readExpectedFix reads the words at pos as a fix where the phraseology
// leaves nothing else to be said: after "direct", after "until". There,
// one word that is no word of the phraseology is the fix that it most
// resembles, however faintly, if one fix stands out from the others. A
// fix that the controller named earlier in the transmission stands out
// by that: "two miles from Vocus, [...] until Voci".
func (d *decoding) readExpectedFix(pos int) []match {
	if ms := d.readFixAs(pos, true); len(ms) > 0 || !d.isPlainWord(pos) {
		return ms
	}
	var best name
	var bestQuality, nextQuality float32
	for _, n := range d.fixNames() {
		q := d.soundsLike(pos, 1, n.spoken) * n.prior
		switch {
		case n.id == best.id:
			bestQuality = max(bestQuality, q)
		case q > bestQuality:
			best, bestQuality, nextQuality = n, q, bestQuality
		default:
			nextQuality = max(nextQuality, q)
		}
	}
	if bestQuality <= similarityFloor {
		return nil
	}
	if bestQuality-nextQuality < expectedNameMargin && !d.namedBefore(pos, best) {
		return nil
	}
	return []match{{end: pos + 1, evidence: best.spoken.weight * gain(bestQuality), values: []any{best.id}}}
}

// namedBefore reports whether the name was said, exactly, after the
// callsign and before pos.
func (d *decoding) namedBefore(pos int, n name) bool {
	span := len(strings.Fields(n.spoken.text))
	for p := d.start; p+span <= pos; p++ {
		if d.spanText(p, p+span) == n.spoken.text {
			return true
		}
	}
	return false
}

// airportNames returns the airports among the aircraft's fixes.
func (d *decoding) airportNames() []name {
	return slices.DeleteFunc(slices.Clone(d.fixNames()), func(n name) bool {
		if db.DB == nil {
			return true
		}
		_, ok := db.DB.Airports[av.ICAOAirportCode(n.id)]
		return !ok || n.prior != 1
	})
}

func (d *decoding) readAirport(pos int) []match { return d.readName(pos, d.airportNames()) }

func (d *decoding) readReportingPoint(pos int) []match {
	return d.readName(pos, makeNames(d.aircraft.ReportingPoints, 1))
}

// readProcedure returns a reader for the name of the aircraft's SID or
// STAR as it is spoken: "Deezz five".
func readProcedure(procedure func(ac *Aircraft) string, telephony func(string) string) func(d *decoding, pos int) []match {
	return func(d *decoding, pos int) []match {
		id := procedure(d.aircraft)
		if id == "" {
			return nil
		}
		spoken := strings.ToLower(telephony(id))
		return d.readName(pos, []name{{spoken: makeVocabWord(spoken), id: id, prior: 1}})
	}
}

var (
	readSID  = readProcedure(func(ac *Aircraft) string { return ac.SID }, speech.GetSIDTelephony)
	readSTAR = readProcedure(func(ac *Aircraft) string { return ac.STAR }, speech.GetSTARTelephony)
)

// isPlainWord reports whether the token at pos is a word that could be a
// name the decoder does not know: not a number, not a word that carries
// no meaning, and not a word of the phraseology.
func (d *decoding) isPlainWord(pos int) bool {
	if pos >= len(d.tokens) {
		return false
	}
	tok := d.tokens[pos]
	return !tok.numeral && !isStopword(tok.text) && !d.phraseology.vocabulary[tok.text] && len(d.atomsAt(pos)) == 0
}

// gapWeight returns how much of the allowance for words passed over
// inside an instruction the token at pos uses. A word of the phraseology
// was probably said and means something, so passing over one uses the
// whole allowance; a word that is not could be anything.
func (d *decoding) gapWeight(pos int) int {
	if tok := d.tokens[pos]; !tok.numeral && d.phraseology.vocabulary[tok.text] {
		return maxGap
	}
	return 1
}

// unknownWordGain is the share of an unknown word's weight that counts as
// evidence that a name was said where one belongs.
const unknownWordGain = 0.5

// readUnknownWord reads one word that is not part of the phraseology, as
// a name that was not understood.
func (d *decoding) readUnknownWord(pos int) []match {
	if !d.isPlainWord(pos) {
		return nil
	}
	return []match{{end: pos + 1, evidence: unknownWordGain * d.tokens[pos].sounds.weight(), values: []any{d.text(pos)}}}
}

// maxNameWords is the most words that a name that was not understood is
// written as.
const maxNameWords = 4

// readUnknownName reads one to maxNameWords words that are not part of the
// phraseology as a name that was not understood. It earns no evidence.
func (d *decoding) readUnknownName(pos int) []match {
	var ms []match
	for end := pos; end < len(d.tokens) && end < pos+maxNameWords; end++ {
		if tok := d.tokens[end]; d.phraseology.vocabulary[tok.text] && !isStopword(tok.text) {
			break
		}
		ms = append(ms, match{end: end + 1, values: []any{""}})
	}
	return ms
}

// readAnyWord reads any one word.
func (d *decoding) readAnyWord(pos int) []match {
	if pos >= len(d.tokens) || d.tokens[pos].numeral {
		return nil
	}
	return []match{{end: pos + 1, values: []any{d.text(pos)}}}
}

// readFacilityWord reads one word of a facility's name. It earns no
// evidence: the name is whatever precedes "approach" or "departure".
func (d *decoding) readFacilityWord(pos int) []match {
	if pos >= len(d.tokens) {
		return nil
	}
	// A word that only resembles a number can be a name; a letter cannot.
	if tok := d.tokens[pos]; len(tok.text) < 2 || isNumberWord(tok.text) || isStopword(tok.text) ||
		d.phraseology.vocabulary[tok.text] {
		return nil
	}
	return []match{{end: pos + 1, values: []any{d.text(pos)}}}
}

// readFacilityFrequency reads a facility name that was not understood
// followed by its frequency: "for ersena one two seven point zero".
func (d *decoding) readFacilityFrequency(pos int) []match {
	const maxWords = 6
	for p := pos; p < len(d.tokens) && p < pos+maxWords; p++ {
		for _, whole := range d.readNumbers(p) {
			if t := d.text(whole.end); t != "point" && t != "decimal" {
				continue
			}
			var best *numberReading
			for _, frac := range d.readNumbers(whole.end + 1) {
				if len(frac.digits) <= 2 && (best == nil || frac.end > best.end) {
					best = &frac
				}
			}
			if best != nil {
				return []match{{end: best.end, evidence: whole.evidence + best.evidence, values: []any{"frequency"}}}
			}
		}
	}
	return nil
}

// readLetter reads a letter of the spelling alphabet.
func (d *decoding) readLetter(pos int) []match {
	letter, end, evidence, ok := d.letterAt(pos)
	if !ok || len(d.text(pos)) == 1 {
		return nil
	}
	return []match{{end: end, evidence: evidence, values: []any{letter}}}
}

// compassPoints are the directions a distance from a fix is given in.
var compassPoints = func() []name {
	var names []name
	for _, p := range [][2]string{{"north", "N"}, {"south", "S"}, {"east", "E"}, {"west", "W"}, {"northeast", "NE"},
		{"northwest", "NW"}, {"southeast", "SE"}, {"southwest", "SW"}} {
		names = append(names, name{spoken: makeVocabWord(p[0]), id: p[1], prior: 1})
	}
	return names
}()

func (d *decoding) readCompass(pos int) []match { return d.readName(pos, compassPoints) }

// airwayLetters are the ways the letter of an airway is said.
var airwayLetters = map[string]string{
	"victor": "V", "v": "V", "tango": "T", "t": "T", "jay": "J", "j": "J", "juliet": "J", "juliett": "J",
	"cue": "Q", "queue": "Q", "q": "Q", "quebec": "Q",
}

// readAirway reads an airway: its letter, then its number.
func (d *decoding) readAirway(pos int) []match {
	letter, ok := airwayLetters[d.text(pos)]
	if !ok {
		return nil
	}
	var ms []match
	for _, r := range d.readNumbers(pos + 1) {
		if v := r.value(); v > 0 && v < 1000 {
			ms = append(ms, match{end: r.end, evidence: d.tokens[pos].sounds.weight() + r.evidence,
				values: []any{letter + strconv.Itoa(v)}})
		}
	}
	return ms
}

// aircraftMakers are words that begin the description of an aircraft
// type.
var aircraftMakers = map[string]bool{
	"boeing": true, "airbus": true, "bus": true, "embraer": true, "cessna": true, "gulfstream": true, "md": true,
}

// readAircraftType reads the description of an aircraft type: a maker, a
// model number, or both ("heavy Boeing triple seven").
func (d *decoding) readAircraftType(pos int) []match {
	end := pos
	var evidence float32
	described := false
	for end < len(d.tokens) {
		tok := d.tokens[end]
		if aircraftMakers[tok.text] || tok.text == "triple" || tok.text == "double" {
			described = true
			evidence += tok.sounds.weight()
			end++
			continue
		}
		if tok.text == "heavy" || tok.text == "heavier" || tok.text == "super" {
			end++
			continue
		}
		// A model number, or after a maker any short number ("bus three
		// three").
		next := d.aircraftTypeNumberEnd(end)
		if next == end && described {
			for _, r := range d.readNumbers(end) {
				if len(r.digits) <= 2 {
					next = max(next, r.end)
				}
			}
		}
		if next == end {
			break
		}
		for _, t := range d.tokens[end:next] {
			evidence += t.sounds.weight()
		}
		described = true
		end = next
	}
	if !described {
		return nil
	}
	return []match{{end: end, evidence: evidence, values: []any{""}}}
}
