// speech/stt/decode.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	av "github.com/mmp/vice/aviation"
)

// This file finds the best interpretation of the words after the
// callsign. An interpretation divides them into instructions, each a
// reading of some of the words as one rule of the phraseology, and words
// that are left unexplained. Its score is the evidence of its
// instructions less a fixed cost for each: a controller says few
// instructions, so a reading has to explain enough sound to be worth
// believing. The best interpretation is found exactly, by dynamic
// programming over the positions in the transcript.

const (
	// instructionCost is the evidence a reading needs before it is
	// believed to be an instruction and not noise.
	instructionCost = 2.0
	// remarkCost is the same for a reading that gives the pilot nothing
	// to do, or that asks for a value to be said again: less is at stake
	// when one is believed wrongly.
	remarkCost = 1.0
)

// tieTolerance is the difference in evidence below which two readings
// are taken to be equally good.
const tieTolerance = 1e-3

// ruleKind says what a rule contributes to the transmission.
type ruleKind uint8

const (
	kindCommand        ruleKind = iota // an instruction to the pilot
	kindInformational                  // said for the pilot's information: "radar contact"
	kindPositionID                     // the controller naming the facility: "New York approach"
	kindSignOff                        // "good day"
	kindAcknowledgment                 // "roger", "hello"
	kindThen                           // "then": the next instruction follows the previous one
	kindDisregard                      // "disregard": retracts what was just said
	kindCorrection                     // "correction": what follows replaces what was said
)

// category groups the commands that assign the same thing, so that a
// restated or corrected instruction can replace the earlier one.
type category uint8

const (
	categoryNone category = iota
	categoryAltitude
	categoryHeading
	categorySpeed
	categoryRoute
	categoryApproachExpect
	categoryApproachClearance
	categoryContact
	categoryTraffic
	categoryLandmark
)

// rule is one production of the phraseology at the level of whole
// instructions. Its production builds the commands, as a string of one
// or more command words separated by spaces, or "" for none.
type rule struct {
	*production
	kind     ruleKind
	category category
	// sequenced rules take the "then" form of their command when they
	// follow "then".
	sequenced bool
	// incomplete marks a rule that stands for the start of an instruction
	// whose value was not understood.
	incomplete bool
	// verbless marks a rule for a value said without the verb that
	// assigns it: "four thousand".
	verbless bool
}

// segment is a reading of tokens [start, end) as a rule.
type segment struct {
	rule       *rule
	start, end int
	evidence   float32
	commands   []string
}

// interpretation is the result of decoding the words after a callsign.
type interpretation struct {
	segments []segment
	evidence float32
}

// interpret returns the best interpretation of the tokens from start. A
// transmission assigns each thing once, so an interpretation holds at
// most one instruction of each category: the search keeps the set of
// categories used so far. A heading is the exception, because a
// controller who restates one is correcting it, and "correction" starts
// the set over. An altitude said in full, in thousands or as a flight
// level, may be said again: controllers restate one ("maintain five
// thousand until established"), and the recognizer repeats whole
// transmissions. A different altitude said in full and with its verb
// replaces it, as a heading does. A misheard speed or a stray number
// does not.
func (d *decoding) interpret(rules []*rule, start int) interpretation {
	type state struct {
		pos  int
		used uint16 // bit set of categories
		// altitude is the altitude assigned so far, in hundreds of feet,
		// if it was said in full, or 0.
		altitude int
	}
	type choice struct {
		evidence float32
		segment  *segment // nil: the token is left unexplained
		next     state
	}
	best := make(map[state]choice)
	var solve func(st state) float32
	solve = func(st state) float32 {
		if st.pos >= len(d.tokens) {
			return 0
		}
		if c, ok := best[st]; ok {
			return c.evidence
		}
		skip := state{st.pos + 1, st.used, st.altitude}
		c := choice{evidence: solve(skip), next: skip}
		for _, r := range rules {
			// A request to say a value again assigns nothing.
			assigns := r.category != categoryNone && !r.incomplete
			bit := uint16(1) << r.category
			if assigns && r.category != categoryHeading && r.category != categoryAltitude && st.used&bit != 0 {
				continue
			}
			for _, m := range r.matches(d, st.pos) {
				alt := 0
				if assigns && r.category == categoryAltitude {
					if d.saysThousands(st.pos, m.end) {
						alt = assignedAltitude(m.values[0].(string))
					}
					if st.used&bit != 0 && (alt == 0 || st.altitude == 0 || alt != st.altitude && r.verbless) {
						continue
					}
				}
				next := state{m.end, st.used, st.altitude}
				// Words that put the next instruction after the one in
				// progress say nothing when no instruction follows them.
				if r.kind == kindThen && solve(next) <= 0 {
					continue
				}
				switch {
				case r.kind == kindCorrection:
					next.used, next.altitude = 0, 0
				case assigns:
					next.used |= bit
					if r.category == categoryAltitude {
						next.altitude = alt
					}
				}
				cost := float32(instructionCost)
				if r.kind != kindCommand || r.incomplete {
					cost = remarkCost
				}
				// Of two readings with the same evidence, the one whose
				// first instruction takes in more words is preferred: a
				// word belongs to the instruction it follows.
				evidence := m.evidence - cost + solve(next)
				if evidence > c.evidence+tieTolerance ||
					evidence > c.evidence-tieTolerance && c.segment != nil && m.end > c.segment.end {
					c = choice{evidence: evidence, next: next, segment: &segment{
						rule: r, start: st.pos, end: m.end, evidence: m.evidence,
						commands: strings.Fields(m.values[0].(string)),
					}}
				}
			}
		}
		best[st] = c
		return c.evidence
	}

	st := state{pos: start}
	in := interpretation{evidence: solve(st)}
	for st.pos < len(d.tokens) {
		c := best[st]
		if c.segment != nil {
			in.segments = append(in.segments, *c.segment)
		}
		st = c.next
	}
	return in
}

// saysThousands reports whether the words [start, end) give an altitude
// in thousands of feet or as a flight level, not as bare digits.
func (d *decoding) saysThousands(start, end int) bool {
	for pos := start; pos < end; pos++ {
		if _, _, ok := d.unitAt(pos, atomThousand); ok || d.text(pos) == "level" || d.text(pos) == "fl" {
			return true
		}
	}
	return false
}

// assignedAltitude returns the altitude, in hundreds of feet, that a
// reading's commands assign, or 0 if they assign none.
func assignedAltitude(cmds string) int {
	for cmd := range strings.FieldsSeq(cmds) {
		if _, alt, ok := altitudeCommand(cmd); ok {
			return alt
		}
	}
	return 0
}

// asksAgain reports whether all a segment does is ask for a value to be
// said again.
func asksAgain(s segment) bool {
	return len(s.commands) > 0 && !slices.ContainsFunc(s.commands, func(cmd string) bool {
		return !strings.HasPrefix(cmd, "SAYAGAIN/")
	})
}

// unexplained reports whether any word that matters lies between two
// positions that the interpretation's segments do not cover.
func (d *decoding) unexplained(from, to int) bool {
	return slices.ContainsFunc(d.tokens[from:to], func(t token) bool { return !isStopword(t.text) })
}

// commands assembles the interpretation's commands in the order they were
// spoken, after applying the words that act on other instructions:
// "then", "disregard" and "correction".
func (d *decoding) commands(in interpretation) (cmds []string, kinds map[ruleKind]bool) {
	kinds = make(map[ruleKind]bool)

	// kept holds the instruction segments that still stand.
	var kept []segment
	then := false
	prevEnd := 0
	for i, s := range in.segments {
		if s.rule.kind != kindCommand {
			kinds[s.rule.kind] = true
		}
		switch s.rule.kind {
		case kindThen:
			then = true

		case kindDisregard:
			// "disregard" retracts the instruction in progress: everything
			// back through the last one that was not finished, or, when the
			// instruction just before it was complete, that instruction.
			if d.unexplained(prevEnd, s.start) && i > 0 {
				for len(kept) > 0 && kept[len(kept)-1].rule.incomplete {
					kept = kept[:len(kept)-1]
				}
			} else if len(kept) > 0 {
				kept = kept[:len(kept)-1]
			}

		case kindCorrection:
			// "Correction" takes back the instruction just before it, and
			// what follows replaces the earlier instruction of its kind;
			// see below. With nothing after it, there is nothing to put in
			// place of what was said, and it stands.
			if len(kept) > 0 && kept[len(kept)-1].rule.kind == kindCommand && i+1 < len(in.segments) {
				kept = kept[:len(kept)-1]
			}
			kept = append(kept, s)

		case kindCommand:
			// The start of an instruction with another instruction right
			// after it is not an instruction whose value was missed: the
			// controller went on to something else.
			if s.rule.incomplete && i+1 < len(in.segments) && in.segments[i+1].rule.kind == kindCommand &&
				!d.unexplained(s.end, in.segments[i+1].start) {
				continue
			}
			// One word at the end of the transmission is too little to
			// ask about.
			if s.rule.incomplete && s.end-s.start == 1 && !d.unexplained(s.end, len(d.tokens)) {
				continue
			}
			kinds[kindCommand] = true
			// A climb or descent that the aircraft's altitude contradicts,
			// with a verb that was not heard exactly: if the verb accounts
			// for everything said before the altitude, the altitude is the
			// part in doubt. If words before it are unexplained, the verb
			// was among words that were garbled, and the altitude stands.
			for j, c := range s.commands {
				if alt, ok := strings.CutPrefix(c, contradictedMarker); ok {
					s.commands[j] = "SAYAGAIN/ALTITUDE"
					if d.unexplained(max(prevEnd, d.start), s.start) {
						s.commands[j] = "A" + alt
					}
				}
			}
			if then && s.rule.sequenced {
				for j, c := range s.commands {
					if !strings.HasPrefix(c, "T") {
						s.commands[j] = "T" + c
					}
				}
			}
			then = false
			kept = append(kept, s)
		}
		prevEnd = s.end
	}

	// After "correction", an instruction replaces the earlier one of the
	// same category.
	for i := 0; i < len(kept); i++ {
		if kept[i].rule.kind != kindCorrection {
			continue
		}
		for _, after := range kept[i+1:] {
			if after.rule.category == categoryNone {
				continue
			}
			for j := 0; j < i; j++ {
				if kept[j].rule.category == after.rule.category {
					kept[j].commands = nil
				}
			}
		}
	}

	for _, s := range kept {
		// A value that was understood elsewhere in the transmission need
		// not be said again.
		if asksAgain(s) && s.rule.category != categoryNone && slices.ContainsFunc(kept, func(o segment) bool {
			return !asksAgain(o) && o.rule.category == s.rule.category && len(o.commands) > 0
		}) {
			continue
		}
		cmds = append(cmds, s.commands...)
	}
	return cmds, kinds
}

// decoding is the state of interpreting the tokens of one transmission as
// instructions to one aircraft. The grammar's patterns are shared and hold
// no state, so everything that depends on the transcript or the aircraft
// lives here.
type decoding struct {
	phraseology *phraseology
	tokens      []token
	aircraft    *Aircraft // the aircraft addressed
	start       int       // position of the first token after the callsign

	memo     map[memoKey][]match
	readings map[int][]numberReading
	atoms    map[int][]numberAtom
	idents   map[int][]identReading

	fixes        []name     // the fixes the aircraft can be sent to
	approachList []approach // the approaches it can be given
}

type memoKey struct {
	pattern pattern
	pos     int
}

func newDecoding(p *phraseology, tokens []token, ac *Aircraft) *decoding {
	if ac == nil {
		ac = &Aircraft{}
	}
	return &decoding{
		phraseology: p,
		tokens:      tokens,
		aircraft:    ac,
		memo:        make(map[memoKey][]match),
		readings:    make(map[int][]numberReading),
		atoms:       make(map[int][]numberAtom),
		idents:      make(map[int][]identReading),
	}
}

// memoized returns the readings of a pattern at a position, computing
// them once.
func (d *decoding) memoized(p pattern, pos int, compute func() []match) []match {
	key := memoKey{pattern: p, pos: pos}
	if ms, ok := d.memo[key]; ok {
		return ms
	}
	// Mark the entry first so that a pattern that refers to itself cannot
	// recurse without consuming a token.
	d.memo[key] = nil
	ms := compute()
	d.memo[key] = ms
	return ms
}

// text returns the text of the token at pos, or "" past the end.
func (d *decoding) text(pos int) string {
	if pos < 0 || pos >= len(d.tokens) {
		return ""
	}
	return d.tokens[pos].text
}

// spanText returns the tokens from pos to end joined by spaces.
func (d *decoding) spanText(pos, end int) string {
	if end-pos == 1 {
		return d.tokens[pos].text
	}
	return strings.Join(tokenTexts(d.tokens[pos:end]), " ")
}

// spanSimilarity returns how closely the span of tokens starting at pos
// sounds like the target word. A numeral only matches itself: "13" does
// not sound like anything but thirteen.
func (d *decoding) spanSimilarity(pos, span int, target vocabWord) float32 {
	if pos+span > len(d.tokens) {
		return 0
	}
	for _, t := range d.tokens[pos : pos+span] {
		if t.numeral {
			if span == 1 && t.text == target.text {
				return 1
			}
			return 0
		}
	}
	return d.soundsLike(pos, span, target)
}

// soundsLike returns how closely the span of tokens starting at pos
// sounds like the target. A word that may be an initialism is tried both
// as a word and as the names of its letters.
func (d *decoding) soundsLike(pos, span int, target vocabWord) float32 {
	q := similarity(d.spanText(pos, pos+span), target)
	if !slices.ContainsFunc(d.tokens[pos:pos+span], func(t token) bool { return t.initialism }) {
		return q
	}
	var spelled []string
	for _, t := range d.tokens[pos : pos+span] {
		if t.initialism {
			spelled = append(spelled, strings.Split(t.text, "")...)
		} else {
			spelled = append(spelled, t.text)
		}
	}
	return max(q, similarity(strings.Join(spelled, " "), target))
}

// similarityCache memoizes similarity: transcripts and phraseology repeat
// the same words.
var similarityCache sync.Map // similarityKey -> float32

type similarityKey struct {
	heard, target string
}

// similarity returns how closely the heard words sound like the target,
// from 0 to 1. Identical text scores 1. A known confusion of the
// recognizer scores as listed; anything else scores by sound, a little
// below 1 even when the sounds are identical, so that the word that was
// written always outranks its homophones.
func similarity(heard string, target vocabWord) float32 {
	if heard == target.text {
		return 1
	}
	key := similarityKey{heard: heard, target: target.text}
	if s, ok := similarityCache.Load(key); ok {
		return s.(float32)
	}

	s := confusionQuality(heard, target.text)
	if s == 0 {
		sounds := pronouncePhrase(heard)
		// The small words of the language resemble each other and carry
		// no sound of their own to tell them apart: one is heard exactly
		// or not at all.
		if isFunctionWord(target.text) && !slices.Equal(sounds, target.sounds) {
			similarityCache.Store(key, float32(0))
			return 0
		}
		// The alignment cost is at least the difference in weight, which
		// bounds the similarity from above.
		if hw := sounds.weight(); min(hw, target.weight) > similarityFloor*max(hw, target.weight) {
			s = min(soundSimilarity(sounds, target.sounds), homophoneQuality)
		}
	}
	similarityCache.Store(key, s)
	return s
}

// homophoneQuality is the similarity of two different words that sound
// the same.
const homophoneQuality = 0.95

// This file holds the rules about how the commands of one transmission
// relate to each other. Each is applied to the list of commands after
// the instructions are read.

// altitudeCommand splits a climb, descend or maintain command, possibly
// in its "then" form, into its letter and altitude.
func altitudeCommand(cmd string) (letter byte, alt int, ok bool) {
	cmd = strings.TrimPrefix(cmd, "T")
	if len(cmd) < 2 || !strings.ContainsRune("CDA", rune(cmd[0])) || !isDigits(cmd[1:]) {
		return 0, 0, false
	}
	alt, _ = strconv.Atoi(cmd[1:])
	return cmd[0], alt, true
}

// isAbsoluteHeading reports whether cmd assigns a heading (H270, L090),
// as opposed to a turn by degrees or a direct-to.
func isAbsoluteHeading(cmd string) bool {
	return len(cmd) >= 2 && strings.ContainsRune("HLR", rune(cmd[0])) && isDigits(cmd[1:])
}

// isDirect reports whether cmd sends the aircraft to a fix.
func isDirect(cmd string) bool {
	for _, prefix := range []string{"LD", "RD", "D"} {
		if rest, ok := strings.CutPrefix(cmd, prefix); ok && rest != "" && !isDigits(rest) && cmd != "DVS" &&
			!strings.HasPrefix(cmd, "DVS/") {
			return true
		}
	}
	return cmd == "SAYAGAIN/FIX"
}

// isApproachClearance reports whether cmd clears the aircraft for an
// approach.
func isApproachClearance(cmd string) bool {
	return len(cmd) > 1 && cmd[0] == 'C' && cmd != "CAC" && cmd != "CWT" && cmd != "CVS" && !isDigits(cmd[1:]) &&
		!strings.Contains(cmd, "/")
}

// compose applies the rules that relate the commands of a transmission
// to each other. ac is the aircraft they are for.
func compose(cmds []string, ac *Aircraft) []string {
	// An expedite or good rate said just before an altitude is for that
	// altitude. The pilot takes the altitude first, because an altitude
	// restated after a rate cancels it.
	for i := 0; i+1 < len(cmds); i++ {
		if !slices.Contains([]string{expediteMarker, "ED", "EC", "GRD", "GRC"}, cmds[i]) {
			continue
		}
		if _, _, ok := altitudeCommand(cmds[i+1]); ok && !strings.HasPrefix(cmds[i+1], "T") {
			cmds[i], cmds[i+1] = cmds[i+1], cmds[i]
			i++
		}
	}

	// "Expedite" alone takes its direction from the altitude instruction
	// before it, and means a descent when there is none.
	for i, cmd := range cmds {
		if cmd != expediteMarker {
			continue
		}
		cmds[i] = "ED"
		for _, prev := range slices.Backward(cmds[:i]) {
			if letter, alt, ok := altitudeCommand(prev); ok {
				if letter == 'C' || letter == 'A' && alt*100 > ac.Altitude {
					cmds[i] = "EC"
				}
				break
			}
		}
	}

	// A second heading restates the first: the controller corrected it.
	if last := lastIndexFunc(cmds, isAbsoluteHeading); last >= 0 {
		for i := range cmds[:last] {
			if isAbsoluteHeading(cmds[i]) {
				cmds[i] = ""
			}
		}
	}

	// "Maintain that until Dennis" ends the speed assigned before it.
	for i, cmd := range cmds {
		fix, ok := strings.CutPrefix(cmd, untilFixMarker)
		if !ok {
			continue
		}
		cmds[i] = ""
		for j, prev := range slices.Backward(cmds[:i]) {
			if len(prev) > 1 && prev[0] == 'S' && isDigits(prev[1:]) {
				cmds[j] += "/U" + fix
				break
			}
		}
	}

	// After "cross FIX at ALT", a climb or descent starts at the fix.
	for i := 0; i+1 < len(cmds); i++ {
		fix, crossAlt, ok := plainCrossing(cmds[i])
		if !ok {
			continue
		}
		if letter, alt, ok := altitudeCommand(cmds[i+1]); ok {
			if letter == 'A' {
				letter = 'D'
				if crossAlt > 0 && alt > crossAlt {
					letter = 'C'
				}
			}
			cmds[i+1] = fmt.Sprintf("A%s/%c%d", fix, letter, alt)
			i++
		}
	}

	// The last altitude said is the one assigned. One said before it with
	// another value was replaced, and one with the same value is given
	// once. This comes after the crossings, because a climb or descent
	// that starts at a fix is a separate instruction.
	isAltitude := func(cmd string) bool { _, _, ok := altitudeCommand(cmd); return ok }
	if last := lastIndexFunc(cmds, isAltitude); last >= 0 {
		_, final, _ := altitudeCommand(cmds[last])
		given := false
		for i, cmd := range cmds {
			if _, alt, ok := altitudeCommand(cmd); ok {
				if given || alt != final {
					cmds[i] = ""
				}
				given = given || alt == final
			}
		}
	}

	cmds = slices.DeleteFunc(cmds, func(cmd string) bool { return cmd == "" })

	// A heading and a direct-to cannot both be flown: the route is the
	// instruction, and the heading was misheard. An approach clearance
	// includes intercepting its final approach course, and it settles
	// where the aircraft goes when a fix before it was not understood.
	direct := slices.ContainsFunc(cmds, isDirect)
	clearance := slices.ContainsFunc(cmds, isApproachClearance)
	cmds = slices.DeleteFunc(cmds, func(cmd string) bool {
		return direct && isAbsoluteHeading(cmd) || clearance && (cmd == "I" || cmd == "SAYAGAIN/FIX")
	})

	// "Approved" answers a request and says nothing next to an
	// instruction.
	if len(cmds) > 1 {
		cmds = slices.DeleteFunc(cmds, func(cmd string) bool { return cmd == approvedMarker })
	}

	// A command said twice, whether restated by the controller or repeated
	// by the recognizer, is carried out once.
	seen := make(map[string]bool)
	return slices.DeleteFunc(cmds, func(cmd string) bool {
		dup := seen[cmd]
		seen[cmd] = true
		return dup
	})
}

func lastIndexFunc(cmds []string, f func(string) bool) int {
	for i, cmd := range slices.Backward(cmds) {
		if f(cmd) {
			return i
		}
	}
	return -1
}

// plainCrossing splits a command to cross a fix ("CROSLY/A60") into the
// fix and the altitude, if it has one. A crossing at a distance from the
// fix or from the runway is not plain.
func plainCrossing(cmd string) (fix string, alt int, ok bool) {
	parts := strings.Split(cmd, "/")
	if len(parts) < 2 || len(parts[0]) < 2 || parts[0][0] != 'C' || isDigits(parts[0][1:]) ||
		strings.HasPrefix(parts[0], "CDME") || cmd == "CVS" || strings.HasPrefix(cmd, "CVS/") {
		return "", 0, false
	}
	if _, _, err := av.ParseDistanceDirection(parts[1]); err == nil {
		return "", 0, false
	}
	for _, part := range parts[1:] {
		if digits := strings.TrimRight(strings.TrimPrefix(part, "A"), "+-"); part[0] == 'A' && isDigits(digits) {
			alt, _ = strconv.Atoi(digits)
		}
	}
	return parts[0][1:], alt, true
}
