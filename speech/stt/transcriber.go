// speech/stt/transcriber.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package stt turns the speech recognizer's transcript of a controller's
// transmission into the commands for the aircraft it addresses.
//
// The decoder treats the transcript as a noisy rendering of what the
// controller said. What the controller can say is known: a callsign of an
// aircraft on the frequency, then instructions in standard phraseology
// about the fixes, procedures and approaches of that aircraft. The
// decoder searches for the callsign and instructions that best explain
// the sound of the transcript, where "explain" is measured by how closely
// the words that were written sound like the words of the hypothesis.
package stt

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mmp/vice/log"
)

// Transcriber converts speech transcripts to aircraft control commands.
type Transcriber struct {
	lg *log.Logger
}

// NewTranscriber creates a new STT transcriber.
func NewTranscriber(lg *log.Logger) *Transcriber {
	return &Transcriber{lg: lg}
}

// Commands that lead the output when the controller revised what they had
// just transmitted. The simulator decides what to undo: CORRECTION
// retracts the previous transmission only if it went to a different
// aircraft, ROLLBACK always does.
const (
	correctionCommand = "CORRECTION"
	rollbackCommand   = "ROLLBACK"
)

// DecodeTranscript converts a speech transcript to aircraft control commands.
// It returns one of:
//   - "{CALLSIGN} {CMD1} {CMD2} ..." for successful parsing
//   - "{CALLSIGN} AGAIN" if callsign identified but commands unclear
//   - "{CALLSIGN} CORRECTION {commands}" for a transmission that revises the previous one
//   - "{CALLSIGN} ROLLBACK" for one that retracts it outright
//   - "" if transcript is empty, asks nothing of the pilot, or no callsign could be matched
//
// Commands may include SAYAGAIN/TYPE where the words that open an
// instruction were recognized but its value was not (e.g., "fly heading
// blark" gives "SAYAGAIN/HEADING"). The aircraft carries out the other
// commands and asks for the missed part again.
//
// controllerRadioName is the user's controller radio name; it is not
// used.
func (t *Transcriber) DecodeTranscript(aircraft map[string]Aircraft, transcript string, controllerRadioName string) (string, error) {
	start := time.Now()
	logLocalStt("=== DecodeTranscript START ===")
	logLocalStt("input transcript: %q", transcript)

	out := decodeTransmission(aircraft, tokenize(transcript))

	logLocalStt("=== DecodeTranscript END: %q (time=%s) ===", out, time.Since(start))
	if t.lg != nil {
		t.lg.Infof("local STT: %q -> %q (time=%s)", transcript, out, time.Since(start))
	}
	return out, nil
}

// DecodeCommandsForCallsign parses commands from a transcript for a known
// callsign. This is used when the controller repeats a command without
// saying the callsign after an aircraft replied "AGAIN".
// Returns one of:
//   - "{commands}" for successfully parsed commands
//   - "AGAIN" if no commands could be parsed
//   - "" if transcript is empty
func (t *Transcriber) DecodeCommandsForCallsign(aircraft map[string]Aircraft, transcript string, callsign string) (string, error) {
	tokens := tokenize(transcript)
	if len(tokens) == 0 {
		return "", nil
	}
	ac, ok := aircraft[callsign]
	if !ok {
		return "AGAIN", nil
	}
	r := readInstructions(tokens, 0, &ac)
	if !r.understood {
		return "AGAIN", nil
	}
	return strings.Join(r.commands, " "), nil
}

// GetUsageStats returns usage statistics for this provider.
func (t *Transcriber) GetUsageStats() string {
	return "local algorithmic parser (no API calls)"
}

// instructions is what was made of the words after a callsign.
type instructions struct {
	commands []string
	kinds    map[ruleKind]bool
	evidence float32
	// understood is true if any of the words were read as part of the
	// phraseology, whether or not they gave the pilot something to do.
	understood bool
}

// readInstructions decodes the tokens from start as instructions to an
// aircraft.
func readInstructions(tokens []token, start int, ac *Aircraft) instructions {
	p := thePhraseology()
	d := newDecoding(p, tokens, ac)
	d.start = start
	in := d.interpret(p.rules, start)
	cmds, kinds := d.commands(in)
	for _, s := range in.segments {
		logLocalStt("  [%d,%d) %q -> %v (evidence %.2f)", s.start, s.end, s.rule.template, s.commands, s.evidence)
	}
	// Words that only act on other instructions say nothing themselves.
	understood := false
	for kind := range kinds {
		understood = understood || kind != kindThen && kind != kindDisregard && kind != kindCorrection
	}
	return instructions{commands: compose(cmds), kinds: kinds, evidence: in.evidence, understood: understood}
}

// lastAddressed returns the aircraft the controller most recently
// transmitted to. A correction that names no aircraft is for them.
func lastAddressed(addressees []addressee) *addressee {
	if i := slices.IndexFunc(addressees, func(a addressee) bool { return a.aircraft.LastAddressed }); i >= 0 {
		return &addressees[i]
	}
	return nil
}

// decodeTransmission decodes the tokens of one transmission.
func decodeTransmission(aircraft map[string]Aircraft, tokens []token) string {
	// "Break, break" separates messages to different aircraft. Only one
	// response is possible; decode the message the controller ended with.
	for i := len(tokens) - 2; i >= 0; i-- {
		if tokens[i].text == "break" && tokens[i+1].text == "break" {
			tokens = tokens[i+2:]
			break
		}
	}
	logLocalStt("tokens: %v", tokenTexts(tokens))
	if len(tokens) == 0 || len(aircraft) == 0 {
		return ""
	}

	addressees := makeAddressees(aircraft)
	last := lastAddressed(addressees)
	rollback := func(fallback *addressee) string {
		if last != nil {
			return last.aircraft.Callsign + " " + rollbackCommand
		}
		if fallback != nil {
			return fallback.aircraft.Callsign + " " + rollbackCommand
		}
		return ""
	}
	lead := func(tokens []token) int {
		return slices.IndexFunc(tokens, func(t token) bool { return !isStopword(t.text) })
	}

	// How the transmission opens says whether it revises the previous
	// one.
	// unaddressed is set when the opening allows the callsign to be left
	// out: the transmission is then for the aircraft addressed last.
	correction, unaddressed := false, false
	if i := lead(tokens); i >= 0 && tokens[i].text == "correction" {
		correction = true
		tokens = tokens[i+1:]
	}
	if i := indexPhrase(tokens, 4, "not", "for", "you"); i >= 0 {
		// "That was not for you" retracts the previous transmission.
		tokens = tokens[i+3:]
		if lead(tokens) < 0 {
			return rollback(nil)
		}
		correction = true
	}
	if i := lead(tokens); i >= 0 && (tokens[i].text == "negative" || tokens[i].text == "no") && i+1 < len(tokens) {
		rest := tokens[i+1:]
		j := -1
		if indexPhrase(rest, 1, "that", "was", "for") == 0 {
			j = 3
		} else if indexPhrase(rest, 1, "was", "for") == 0 {
			j = 2
		}
		if j > 0 {
			// "Negative, that was for Delta four five six": the previous
			// transmission went to the wrong aircraft, and the rest of this
			// one is for the right one.
			d := newDecoding(thePhraseology(), rest[j:], nil)
			ms := d.matchCallsigns(addressees)
			if len(ms) == 0 {
				return rollback(nil)
			}
			m := ms[0]
			r := readInstructions(rest[j:], m.end, &m.addressee.aircraft)
			if len(r.commands) == 0 {
				return rollback(m.addressee)
			}
			return m.addressee.aircraft.Callsign + " " + correctionCommand + " " + strings.Join(r.commands, " ")
		}
		unaddressed = true
		tokens = rest
	}
	if lead(tokens) < 0 {
		return ""
	}

	// The callsign comes first, and it alone decides which aircraft is
	// addressed: nothing said after it changes that. A correction that
	// opens with an instruction names no aircraft and is for the one that
	// was addressed last.
	d := newDecoding(thePhraseology(), tokens, nil)
	if i := lead(tokens); (correction || unaddressed) && last != nil && thePhraseology().vocabulary[tokens[i].text] {
		c := callsignMatch{addressee: last, name: 1, ident: 1}
		return assemble(c, readInstructions(tokens, 0, &last.aircraft), true)
	}
	// restart decodes the transmission from the callsign after a
	// "correction" in rest, if there is one: the controller started over.
	// immediate says that rest follows a callsign, so that a "correction"
	// at its start corrects that callsign.
	restart := func(rest []token, immediate bool) (string, bool) {
		for i, tok := range rest {
			if tok.text != "correction" || i+1 >= len(rest) {
				continue
			}
			sub := newDecoding(thePhraseology(), rest[i+1:], nil)
			if ms := sub.matchCallsigns(addressees); len(ms) > 0 && ms[0].start == 0 &&
				(ms[0].certain() || immediate && i == 0 && ms[0].ident == 1) {
				return decodeTransmission(aircraft, rest[i+1:]), true
			}
		}
		return "", false
	}

	candidates := d.matchCallsigns(addressees)
	if len(candidates) == 0 {
		if (correction || unaddressed) && last != nil {
			c := callsignMatch{addressee: last, name: 1, ident: 1}
			return assemble(c, readInstructions(tokens, 0, &last.aircraft), true)
		}
		out, _ := restart(tokens, false)
		return out
	}

	// The same callsign can be read as ending at different words, when
	// what follows it could be more of its number. The reading that
	// leaves the instructions making the most sense is the right one.
	// An identifier that was heard exactly is the whole callsign: no
	// reading that takes in more words competes with it.
	exact := slices.ContainsFunc(candidates, func(c callsignMatch) bool {
		return c.addressee.aircraft.Callsign == candidates[0].addressee.aircraft.Callsign && c.ident == 1
	})
	var best callsignMatch
	var bestInstructions instructions
	for _, c := range candidates {
		if c.addressee.aircraft.Callsign != candidates[0].addressee.aircraft.Callsign || exact && c.ident < 1 {
			continue
		}
		r := readInstructions(tokens, c.end, &c.addressee.aircraft)
		logLocalStt("callsign %s [%d,%d): evidence %.2f (name %.2f, ident %.2f), instructions %.2f %v",
			c.addressee.aircraft.Callsign, c.start, c.end, c.evidence, c.name, c.ident, r.evidence, r.commands)
		if best.addressee == nil || c.evidence+r.evidence > best.evidence+bestInstructions.evidence {
			best, bestInstructions = c, r
		}
	}

	// After the callsign, "correction" and another callsign starts the
	// transmission over, and "not for you" withdraws it from this aircraft.
	rest := tokens[best.end:]
	if out, ok := restart(rest, true); ok {
		return out
	}
	if i := indexPhrase(rest, 4, "not", "for", "you"); i >= 0 {
		rest = rest[i+3:]
		sub := newDecoding(thePhraseology(), rest, nil)
		ms := sub.matchCallsigns(addressees)
		if len(ms) == 0 {
			return best.addressee.aircraft.Callsign + " " + rollbackCommand
		}
		return assemble(ms[0], readInstructions(rest, ms[0].end, &ms[0].addressee.aircraft), true)
	}

	return assemble(best, bestInstructions, correction)
}

// indexPhrase returns the index at which the words appear in order in
// tokens, looking no further than the first within positions, or -1.
func indexPhrase(tokens []token, within int, words ...string) int {
	for i := 0; i < within && i+len(words) <= len(tokens); i++ {
		if slices.Equal(tokenTexts(tokens[i:i+len(words)]), words) {
			return i
		}
	}
	return -1
}

// assemble builds the response for a callsign and its instructions.
func assemble(c callsignMatch, r instructions, correction bool) string {
	callsign := c.addressee.aircraft.Callsign
	cmds := r.commands

	// Asking only for a fix again commits to an aircraft as much as
	// answering for it does; see below.
	if !c.certain() && !slices.ContainsFunc(cmds, func(cmd string) bool { return cmd != "SAYAGAIN/FIX" }) {
		cmds = nil
	}

	switch {
	case len(cmds) > 0:
		// The correction applies to instructions; a request to hear
		// something again revises nothing.
		if correction && slices.ContainsFunc(cmds, func(cmd string) bool { return !strings.HasPrefix(cmd, "SAYAGAIN") }) {
			return callsign + " " + correctionCommand + " " + strings.Join(cmds, " ")
		}
		return callsign + " " + strings.Join(cmds, " ")

	case r.kinds[kindDisregard]:
		// The controller took back what they said.
		return ""

	case r.understood && !r.kinds[kindCommand]:
		// Nothing was asked of the pilot.
		switch {
		case r.kinds[kindPositionID] && r.kinds[kindSignOff]:
			// The controller named a facility and signed off: a handoff.
			if correction {
				return callsign + " " + correctionCommand + " FC"
			}
			return callsign + " FC"
		case r.kinds[kindAcknowledgment]:
			return ""
		case r.kinds[kindPositionID] && c.addressee.aircraft.State == stateVFR:
			// A VFR pilot addressed with only the facility name is being
			// invited to make their request.
			if correction {
				return callsign + " " + correctionCommand + " GA"
			}
			return callsign + " GA"
		}
		return ""

	case r.understood || !c.certain():
		// Words that need no action get no answer: "standby for the
		// approach". Nor do words that were not understood when the
		// aircraft is in doubt: answering for the wrong aircraft is worse
		// than silence.
		return ""
	}
	return callsign + " AGAIN"
}

// token is one word of the transcript, prepared for matching.
type token struct {
	text    string        // lower case, with punctuation removed
	sounds  pronunciation // how the word sounds
	numeral bool          // text is a string of digits ("270")
	// initialism is set for a short word that the recognizer wrote in
	// capitals ("LSA"): it may have heard letters and not a word.
	initialism bool
}

// tokenize splits a transcript into tokens. Punctuation is dropped, a word
// that mixes letters and digits is split at each change ("alaska8383",
// "22L"), and "point" is restored between the halves of a decimal.
func tokenize(transcript string) []token {
	var tokens []token
	for field := range strings.FieldsSeq(transcript) {
		field = strings.Map(unaccent, field)
		words := splitField(strings.ToLower(field))
		for _, w := range words {
			tokens = append(tokens, token{text: w, sounds: pronounce(w), numeral: isDigits(w),
				initialism: len(words) == 1 && isInitialism(field)})
		}
	}
	return collapseLoops(tokens)
}

// unaccent returns a letter without its accent: the recognizer writes
// some names in the spelling of their language.
func unaccent(r rune) rune {
	const accented, plain = "àáâãäåçèéêëìíîïñòóôõöùúûüýÀÁÂÃÄÅÇÈÉÊËÌÍÎÏÑÒÓÔÕÖÙÚÛÜÝ", "aaaaaaceeeeiiiinooooouuuuyAAAAAACEEEEIIIINOOOOOUUUUY"
	if i := strings.IndexRune(accented, r); i >= 0 {
		return rune(plain[utf8.RuneCountInString(accented[:i])])
	}
	return r
}

// isInitialism reports whether a field of the transcript is two to four
// capital letters that are not a word the decoder knows how to say.
func isInitialism(field string) bool {
	field = strings.TrimFunc(field, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(field) < 2 || len(field) > 4 || strings.ContainsFunc(field, func(r rune) bool { return r < 'A' || r > 'Z' }) {
		return false
	}
	_, known := lexicon()[strings.ToLower(field)]
	return !known
}

// splitField splits one whitespace-delimited field of the transcript into
// words.
func splitField(field string) []string {
	field = strings.ReplaceAll(field, "@", " at ")

	var words []string
	var cur strings.Builder
	var curDigits bool
	// afterDigits is set while reading letters that directly follow digits.
	afterDigits := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		// Letters written after digits are the letters of an identifier
		// ("4WJ"), each said on its own.
		if n := len(words); !curDigits && n > 0 && isDigits(words[n-1]) && afterDigits {
			for _, r := range cur.String() {
				words = append(words, string(r))
			}
		} else {
			words = append(words, cur.String())
		}
		cur.Reset()
	}
	runes := []rune(field)
	for i, r := range runes {
		switch {
		case unicode.IsDigit(r):
			if cur.Len() > 0 && !curDigits {
				flush()
			}
			curDigits = true
			cur.WriteRune(r)
		case unicode.IsLetter(r):
			if cur.Len() > 0 && curDigits {
				flush()
				afterDigits = true
			}
			curDigits = false
			cur.WriteRune(r)
		case r == '.' && curDigits && i+1 < len(runes) && unicode.IsDigit(runes[i+1]):
			flush()
			words = append(words, "point")
		case r == '\'':
			// "o'clock" and "that's" are single words.
		default:
			flush()
			afterDigits = false
		}
	}
	flush()

	// A runway written as a numeral and a letter ("22L") is spoken as the
	// number and the side.
	if n := len(words); n >= 2 && isDigits(words[n-2]) && len(words[n-2]) <= 2 {
		if side, ok := map[string]string{"l": "left", "r": "right", "c": "center"}[words[n-1]]; ok {
			words[n-1] = side
		}
	}
	return words
}

// collapseLoops reduces three or more consecutive repetitions of a one- or
// two-word group to a single occurrence: the recognizer loops on unclear
// audio. Repeated numbers are left alone because they are real content
// ("seven seven seven").
func collapseLoops(tokens []token) []token {
	sameText := func(a, b token) bool { return a.text == b.text }
	for group := 2; group >= 1; group-- {
		var out []token
		for i := 0; i < len(tokens); {
			reps := 1
			for i+(reps+1)*group <= len(tokens) &&
				slices.EqualFunc(tokens[i:i+group], tokens[i+reps*group:i+(reps+1)*group], sameText) {
				reps++
			}
			if reps >= 3 && !isNumberWord(tokens[i].text) {
				out = append(out, tokens[i:i+group]...)
				i += reps * group
			} else {
				out = append(out, tokens[i])
				i++
			}
		}
		tokens = out
	}
	return tokens
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// tokenTexts returns the text of each token, for logging.
func tokenTexts(tokens []token) []string {
	texts := make([]string, len(tokens))
	for i, t := range tokens {
		texts[i] = t.text
	}
	return texts
}
