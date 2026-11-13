// speech/stt/approach.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// This file reads approach names. An approach is spoken as its kind, the
// word "runway", the runway number and its side: "I L S runway two two
// left", "R-NAV zulu runway two eight right", "River Visual runway one
// niner". The aircraft's context lists the approaches it can be given, so
// reading one is a choice among those candidates: each is scored by how
// well the words fit its kind, its runway and its side, and the
// approach the aircraft was told to expect is the likeliest.

// approach is one approach the aircraft can be given.
type approach struct {
	id       string
	fullName string
	kind     vocabWord // what is said before "runway": "ils", "rnav zulu"
	// short holds the shorter ways the kind is said: without its variant
	// letter ("rnav" for "rnav zulu"), and its first word alone ("rnav"
	// for "rnav rnp").
	short  []vocabWord
	runway string // the runway number
	side   string // "left", "right", "center", or "" for none
}

// approachWords gives the spoken form of the abbreviations in approach
// names.
var approachWords = map[string]string{
	"loc": "localizer", "hi-tacan": "tacan",
}

var sideWords = map[string]string{"l": "left", "r": "right", "c": "center", "left": "left", "right": "right", "center": "center"}

// parseApproach splits the full name of an approach ("RNAV Z Runway
// 28R") into the parts that are spoken.
func parseApproach(fullName, id string) approach {
	a := approach{id: id, fullName: fullName}
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(fullName, ",", " ")))
	i := slices.Index(words, "runway")
	if i < 0 {
		a.kind = makeVocabWord(strings.Join(words, " "))
		return a
	}

	var kind, plain []string
	for _, w := range words[:i] {
		if spoken, ok := approachWords[w]; ok {
			w = spoken
		}
		if len(w) == 1 && w[0] >= 'a' && w[0] <= 'z' {
			// A variant letter is said in the alphabet.
			kind = append(kind, alphabet[w[0]-'a'].text)
			continue
		}
		kind = append(kind, w)
		plain = append(plain, w)
	}
	a.kind = makeVocabWord(strings.Join(kind, " "))
	if len(plain) > 0 && len(plain) < len(kind) {
		a.short = append(a.short, makeVocabWord(strings.Join(plain, " ")))
	}
	if len(plain) > 1 {
		a.short = append(a.short, makeVocabWord(plain[0]))
	}

	if i+1 < len(words) {
		rwy := words[i+1]
		digits := strings.TrimRight(rwy, "lrc")
		a.runway = digits
		a.side = sideWords[rwy[len(digits):]]
		if a.side == "" && i+2 < len(words) {
			a.side = sideWords[words[i+2]]
		}
	}
	return a
}

// runwayID returns the approach's runway as it is written: "22L".
func (a approach) runwayID() string {
	return a.runway + strings.ToUpper(a.side[:min(1, len(a.side))])
}

// visualRunways returns the runways in use for visual approaches.
func (d *decoding) visualRunways() map[string]bool {
	runways := make(map[string]bool)
	for _, rwy := range d.aircraft.CandidateVisualApproaches {
		runways[rwy] = true
	}
	return runways
}

// approaches returns the approaches the aircraft can be given, in order
// of their identifiers.
func (d *decoding) approaches() []approach {
	if d.approachList == nil {
		d.approachList = []approach{}
		for fullName, id := range d.aircraft.CandidateApproaches {
			d.approachList = append(d.approachList, parseApproach(fullName, id))
		}
		slices.SortFunc(d.approachList, func(a, b approach) int {
			return cmp.Or(cmp.Compare(a.id, b.id), cmp.Compare(a.fullName, b.fullName))
		})
	}
	return d.approachList
}

// assignedApproach returns the approach the aircraft was told to expect.
func (d *decoding) assignedApproach() (approach, bool) {
	for _, a := range d.approaches() {
		if strings.EqualFold(a.fullName, d.aircraft.AssignedApproach) {
			return a, true
		}
	}
	return approach{}, false
}

// assignedApproachID returns the identifier of the approach the aircraft
// was told to expect, or "".
func (d *decoding) assignedApproachID() string {
	a, _ := d.assignedApproach()
	return a.id
}

var (
	runwayWord = makeVocabWord("runway")
	sideVocab  = []vocabWord{makeVocabWord("left"), makeVocabWord("right"), makeVocabWord("center")}
)

const (
	// plainKindQuality scales the match of an approach's kind said in one
	// of its shorter ways.
	plainKindQuality = 0.92
	// expectedApproachBonus is the evidence that the approach the aircraft
	// was told to expect is the one meant, before any word is heard.
	expectedApproachBonus = 1.0
	// wrongRunwayQuality is how much of the evidence for a runway number
	// counts when it holds the candidate's number among other digits.
	wrongRunwayQuality = 0.6
	// misheardRunwayQuality is the same for a number with one digit that
	// differs from the candidate's.
	misheardRunwayQuality = 0.5
)

// sideAt reads the word at pos as the side of a runway.
func (d *decoding) sideAt(pos int) (side string, evidence float32, ok bool) {
	var best vocabWord
	var bestQuality float32
	for _, w := range sideVocab {
		if q := d.spanSimilarity(pos, 1, w); q > bestQuality {
			best, bestQuality = w, q
		}
	}
	if bestQuality <= similarityFloor {
		return "", 0, false
	}
	return best.text, best.weight * gain(bestQuality), true
}

// runwayReading is a reading of words as a runway: its number and its
// side.
type runwayReading struct {
	digits   string
	side     string
	end      int
	evidence float32 // evidence for the number
	sideEv   float32 // evidence for the side
}

// readRunways returns the readings of the words at pos as a runway: the
// number, with the side after it or before it.
func (d *decoding) readRunways(pos int) []runwayReading {
	var rs []runwayReading
	start := pos
	var before string
	var beforeEv float32
	if side, ev, ok := d.sideAt(pos); ok {
		// "ILS right four".
		before, beforeEv, start = side, ev, pos+1
	}
	for _, from := range []int{pos, start} {
		for _, r := range d.readNumbers(from) {
			digits := strings.TrimLeft(r.digits, "0")
			if digits == "" {
				continue
			}
			rr := runwayReading{digits: digits, end: r.end, evidence: r.evidence}
			if from != pos {
				rr.side, rr.sideEv = before, beforeEv
			}
			rs = append(rs, rr)
			if side, ev, ok := d.sideAt(r.end); ok && from == pos {
				rr.side, rr.sideEv, rr.end = side, ev, r.end+1
				rs = append(rs, rr)
			}
		}
		if start == pos {
			break
		}
	}
	return rs
}

// onlyOfKind reports whether no other approach of the aircraft has the
// same kind as a.
func (d *decoding) onlyOfKind(a approach) bool {
	return !slices.ContainsFunc(d.approaches(), func(o approach) bool { return o.id != a.id && o.kind.text == a.kind.text })
}

// vagueSideAt reads the word at pos as the side of a runway when a side
// is known to have been said: the side it sounds most like, however
// faintly. It is used when every runway the number could mean has a
// side, so that the word after the number must be one.
func (d *decoding) vagueSideAt(pos int) (side string, ok bool) {
	if pos >= len(d.tokens) || d.tokens[pos].numeral || d.phraseology.vocabulary[d.text(pos)] && !isFunctionWord(d.text(pos)) {
		return "", false
	}
	const least, margin = 0.4, 0.1
	var best, second float32
	for _, w := range sideVocab {
		q := soundSimilarity(d.tokens[pos].sounds, w.sounds)
		if q > best {
			side, best, second = w.text, q, best
		} else if q > second {
			second = q
		}
	}
	return side, best >= least && best-second >= margin
}

// kindEvidence returns the evidence that the span of tokens at pos is the
// kind of the approach, said in full or without its variant letter.
func (d *decoding) kindEvidence(a approach, pos, span int) (evidence float32, ok bool) {
	if span == 0 {
		return 0, true
	}
	q := d.soundsLike(pos, span, a.kind)
	weight := a.kind.weight
	for _, short := range a.short {
		if sq := d.soundsLike(pos, span, short) * plainKindQuality; sq > q {
			q, weight = sq, short.weight
		}
	}
	if q <= similarityFloor {
		return 0, false
	}
	return weight * gain(q), true
}

// runwayLead is a reading of the words between the kind of an approach
// and its runway number.
type runwayLead struct {
	pos      int // where the number starts
	evidence float32
	garbled  bool // words were passed over
	runway   bool // "runway" was heard
}

// maxKindWords is the most words that the kind of an approach is written
// as.
const maxKindWords = maxSpan + 1

// runwayLeads returns what can come between the kind and the runway
// number from p: nothing, "runway", possibly garbled into several words,
// or words that were not understood. With no kind heard, the words that
// were not understood stand for it.
func (d *decoding) runwayLeads(p int) []runwayLead {
	leads := []runwayLead{{pos: p}}
	// runway adds the leads that continue from l with the word "runway".
	runway := func(l runwayLead) {
		for n := 1; n <= maxSpan; n++ {
			if q := d.spanSimilarity(l.pos, n, runwayWord); q > similarityFloor {
				leads = append(leads, runwayLead{pos: l.pos + n, evidence: l.evidence + runwayWord.weight*gain(q),
					garbled: l.garbled, runway: true})
			}
		}
	}
	runway(leads[0])
	var gap float32
	for n := 1; n <= maxKindWords && p+n <= len(d.tokens); n++ {
		tok := d.tokens[p+n-1]
		if tok.numeral || len(d.atomsAt(p+n-1)) > 0 && !isFunctionWord(tok.text) {
			break
		}
		// "Approach" ends the name unless the runway comes after it.
		if tok.text == "approach" && d.text(p+n) != "runway" && len(d.atomsAt(p+n)) == 0 {
			break
		}
		if !isStopword(tok.text) {
			gap += gapCost * tok.sounds.weight()
		}
		l := runwayLead{pos: p + n, evidence: -gap, garbled: true}
		leads = append(leads, l)
		runway(l)
	}
	return leads
}

// isSubsequence reports whether the characters of sub appear in s in
// order.
func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; i < len(sub) && j < len(s); j++ {
		if sub[i] == s[j] {
			i++
		}
	}
	return i == len(sub)
}

// runwayQuality returns how much of the evidence for a runway number that
// was heard counts for the approach: all of it for the approach's own
// number, part of it for that number as the recognizer distorts it, and
// none otherwise. side is the side that was heard with the number, if
// any; last says that no digit follows the number; expected says that the
// approach is the one the aircraft was told to expect.
func (d *decoding) runwayQuality(a approach, heard, side string, last, expected bool) float32 {
	// others reports whether another approach of the same kind, on the
	// side that was heard, also fits what was heard.
	others := func(fits func(runway string) bool) bool {
		return slices.ContainsFunc(d.approaches(), func(o approach) bool {
			return o.id != a.id && o.kind.text == a.kind.text && (side == "" || o.side == side) && fits(o.runway)
		})
	}
	switch {
	case heard == a.runway:
		return 1
	case len(heard) > len(a.runway) && isSubsequence(a.runway, heard):
		// Digits ran into the number.
		return wrongRunwayQuality
	case len(heard) < len(a.runway) && last && (strings.HasPrefix(a.runway, heard) || strings.HasSuffix(a.runway, heard)):
		// The number was cut short.
		if expected || !others(func(runway string) bool {
			return strings.HasPrefix(runway, heard) || strings.HasSuffix(runway, heard)
		}) {
			return wrongRunwayQuality
		}
	case len(heard) == len(a.runway) && side != "" && editDistance(heard, a.runway) == 1:
		// One digit was misheard, if the number that was heard is no
		// runway and no other runway is as close.
		exists := slices.ContainsFunc(d.approaches(), func(o approach) bool { return o.runway == heard })
		if !exists && !others(func(runway string) bool { return editDistance(heard, runway) == 1 }) {
			return misheardRunwayQuality
		}
	}
	return 0
}

// kindAfter reads the kind of the approach after its runway: "runway four
// right, R-NAV zulu". It returns the position after the kind.
func (d *decoding) kindAfter(a approach, pos int) (end int, evidence float32, ok bool) {
	var gap float32
	for skipped := 0; skipped <= maxGap && pos < len(d.tokens); skipped++ {
		for span := 1; span <= maxKindWords && pos+span <= len(d.tokens); span++ {
			if ev, heard := d.kindEvidence(a, pos, span); heard && ev-gap > evidence {
				end, evidence, ok = pos+span, ev-gap, true
			}
		}
		if ok || !d.isPlainWord(pos) {
			break
		}
		gap += gapCost * d.tokens[pos].sounds.weight()
		pos++
	}
	return
}

// readApproach reads the words at pos as one of the aircraft's
// approaches.
func (d *decoding) readApproach(pos int) []match { return d.readApproachFrom(pos, false) }

// readNamedApproach reads the words at pos as an approach whose kind is
// said: where no verb announces an approach, a runway alone is not one.
func (d *decoding) readNamedApproach(pos int) []match { return d.readApproachFrom(pos, true) }

func (d *decoding) readApproachFrom(pos int, named bool) []match {
	assigned, hasAssigned := d.assignedApproach()

	type reading struct {
		approach approach
		evidence float32
	}
	best := make(map[int]reading) // by end position
	// rank orders approaches that the words support equally: the one that
	// is expected, then an ILS, then by identifier.
	rank := func(a approach) int {
		switch {
		case hasAssigned && a.id == assigned.id:
			return 0
		case a.kind.text == "ils":
			return 1
		}
		return 2
	}
	consider := func(a approach, end int, evidence float32) {
		if hasAssigned && a.id == assigned.id {
			evidence += expectedApproachBonus
		}
		cur, ok := best[end]
		if !ok || evidence > cur.evidence || evidence == cur.evidence && rank(a) < rank(cur.approach) {
			best[end] = reading{a, evidence}
		}
	}

	for _, a := range d.approaches() {
		// A visual approach with no name is read as the visual approach to
		// its runway (readVisualRunway) when that runway is in use for
		// them.
		if (a.kind.text == "visual" || a.kind.text == "visual approach") && d.visualRunways()[a.runwayID()] {
			continue
		}
		isAssigned := hasAssigned && a.id == assigned.id
		onAssignedRunway := hasAssigned && a.runway == assigned.runway && a.side == assigned.side

		// The kind, in zero to maxKindWords words.
		for span := 0; span <= maxKindWords && pos+span <= len(d.tokens); span++ {
			kindEv, ok := d.kindEvidence(a, pos, span)
			if !ok {
				continue
			}
			p := pos + span

			runwayFollows := false
			for _, l := range d.runwayLeads(p) {
				for _, r := range d.readRunways(l.pos) {
					runwayFollows = true
					side, end := r.side, r.end
					if side == "" && a.side != "" {
						// A side that follows the number is part of the
						// runway; the reading that takes it in is the one to
						// consider.
						if _, _, heard := d.sideAt(r.end); heard {
							continue
						}
						if s, ok := d.vagueSideAt(r.end); ok {
							side, end = s, r.end+1
						}
					}
					// A side that was heard must be the candidate's.
					if side != "" && side != a.side {
						continue
					}

					last := r.side != "" || len(d.atomsAt(r.end)) == 0
					quality := d.runwayQuality(a, r.digits, r.side, last, isAssigned)
					evidence := kindEv + l.evidence + r.evidence*quality + r.sideEv
					switch {
					case span > 0 && (quality > 0 || isAssigned):
						// The kind and the runway; for the expected approach,
						// the number may not have been understood at all.
					case span > 0:
						continue
					case l.garbled && quality > 0 && r.side != "" && !named:
						// Runway and side, after words where the kind belongs.
					case l.garbled && l.runway && quality == 1 && !named:
						// The same, with "runway" in place of a side.
					case !l.garbled && quality == 1 && onAssignedRunway && !named && !commonHomophoneOnly(d, l.pos, r.end):
						// The runway alone, for the approach that is expected.
					case quality == 1:
						// The runway, then the kind.
						kindEnd, ev, heard := d.kindAfter(a, end)
						if !heard {
							continue
						}
						end, evidence = kindEnd, evidence+ev
					default:
						continue
					}
					consider(a, end, evidence)
				}
				// The kind and the side of the expected approach, with no
				// number: "I L S runway (something) right".
				if span > 0 && isAssigned && l.pos > p {
					if side, ev, ok := d.sideAt(l.pos); ok && side == a.side {
						consider(a, l.pos+1, kindEv+l.evidence+ev)
					}
				}
			}

			// The kind alone names the approach that is expected, and one
			// that is the only approach of its kind, when no runway is said
			// after it.
			if span > 0 && !runwayFollows && (isAssigned || a.runway == "" || d.onlyOfKind(a)) {
				consider(a, p, kindEv)
			}
		}
	}

	var ms []match
	for end, r := range best {
		ms = append(ms, match{end: end, evidence: r.evidence, values: []any{r.approach.id}})
	}
	return ms
}

var visualWord = makeVocabWord("visual")

// readVisualRunway reads "visual approach runway two two left" as the
// runway of a visual approach. The runway must be one that is in use for
// visual approaches, and the words must single it out.
func (d *decoding) readVisualRunway(pos int) []match {
	runways := d.visualRunways()
	if len(runways) == 0 {
		return nil
	}
	// A word before "visual" that is not part of the instruction is the
	// name of a charted visual approach, which is one of the aircraft's
	// approaches (readApproach) and not a visual approach to a runway.
	before := d.text(pos - 1)
	if pos > d.start && !isStopword(before) && !d.phraseology.vocabulary[before] && slices.ContainsFunc(d.approaches(), func(a approach) bool {
		return strings.HasSuffix(a.kind.text, " visual")
	}) {
		return nil
	}

	var ms []match
	if q := d.spanSimilarity(pos, 1, visualWord); q >= nameFloor {
		kindEv := visualWord.weight * gain(q)
		p := pos + 1
		if d.text(p) == "approach" {
			p++
		}
		leads := map[int]float32{p: 0}
		for n := 1; n <= maxSpan; n++ {
			if q := d.spanSimilarity(p, n, runwayWord); q > similarityFloor {
				leads[p+n] = runwayWord.weight * gain(q)
			}
		}
		if d.isPlainWord(p) {
			leads[p+1] = -gapCost * d.tokens[p].sounds.weight()
		}
		for lead, leadEv := range leads {
			for _, r := range d.readRunways(lead) {
				var found []string
				for rwy := range runways {
					digits := strings.TrimRight(rwy, "LRC")
					side := sideWords[strings.ToLower(rwy[len(digits):])]
					number := r.digits
					if len(number) == 3 {
						// "one zero nine": the last digit is a garbled side.
						number = number[:2]
					}
					if n, _ := strconv.Atoi(number); n < 1 || n > 36 {
						continue
					}
					if digits == number && (r.side == "" || r.side == side) {
						found = append(found, rwy)
					}
				}
				if len(found) == 1 {
					ms = append(ms, match{end: r.end, evidence: kindEv + leadEv + r.evidence + r.sideEv, values: []any{found[0]}})
				}
			}
		}
	}
	return ms
}

// readLAHSORunway reads the runway to hold short of. It must be one that
// crosses the landing runway; a runway named from its other end is
// accepted as spoken.
func (d *decoding) readLAHSORunway(pos int) []match {
	var ms []match
	for _, r := range d.readRunways(pos) {
		n, _ := strconv.Atoi(r.digits)
		if n < 1 || n > 36 {
			continue
		}
		letter := strings.ToUpper(r.side[:min(1, len(r.side))])
		spoken := r.digits + letter
		id := ""
		for _, rwy := range d.aircraft.LAHSORunways {
			digits := strings.TrimRight(rwy, "LRC")
			switch {
			case rwy == spoken:
				id = rwy
			case id == "" && digits == r.digits && letter == "":
				id = rwy
			case id == "" && digits == strconv.Itoa((n+17)%36+1):
				id = spoken
			}
			if id == spoken && rwy == spoken {
				break
			}
		}
		if id != "" {
			ms = append(ms, match{end: r.end, evidence: r.evidence + r.sideEv, values: []any{id}})
		}
	}
	return ms
}
