// speech/stt/grammar.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/math"
)

// This file implements the grammar formalism the phraseology is written
// in. A grammar is a set of productions. Each production pairs a template,
// which says what the controller says, with a function that turns the
// values captured from the transcript into a result. Productions with the
// same name are alternatives of one nonterminal, and a template can refer
// to a nonterminal by name, so shared pieces of phraseology ("at or above
// {altitude}") are written once.
//
// Matching is scored, not boolean. A reading of some tokens as a
// production earns evidence for the sound it explains: each word of the
// template that the transcript supports contributes its weight, scaled by
// how closely the transcript matches it. Tokens skipped inside a
// production cost a little. The decoder (decode.go) searches for the set
// of readings with the most total evidence.

// Template syntax:
//
//	word        a word the controller says, matched by sound
//	a|b         alternatives for one word
//	[...]       optional words or an optional group
//	{name}      a slot or a nonterminal, which captures a value
const (
	// similarityFloor is the similarity at which hearing a word is no
	// better evidence for it than noise is. A closer match is positive
	// evidence in proportion to how much closer it is.
	similarityFloor = 0.55

	// gapCost is the evidence lost, per unit of sound, for a word that is
	// skipped inside a production.
	gapCost = 0.25

	// maxGap is how many content words may be skipped between two elements
	// of a production.
	maxGap = 2

	// maxSpan is how many transcript words, or template words, one step of
	// phrase matching can cover: the recognizer splits and merges words.
	maxSpan = 3
)

// gain converts a similarity into the fraction of a word's weight that
// counts as evidence. It rises slowly above the floor and steeply near 1:
// a word that is nearly what was expected is strong evidence for it, and
// one that is only somewhat like it is weak evidence.
func gain(similarity float32) float32 {
	if similarity <= similarityFloor {
		return 0
	}
	return math.Sqr((similarity - similarityFloor) / (1 - similarityFloor))
}

// doubtWeight is how much a poor match of a word that a production
// requires counts against the production.
const doubtWeight = 0.5

// support converts the similarity of a required word into the fraction
// of its weight that counts for the production. Unlike gain it can be
// negative: a word that only faintly resembles the one the production
// needs is more likely some other word, and that is evidence against the
// production.
func support(similarity float32) float32 {
	t := (similarity - similarityFloor) / (1 - similarityFloor)
	return math.Sqr(t) - doubtWeight*math.Sqr(1-t)
}

// anchorQuality is the similarity at which the first word of a production
// is heard well enough to carry the whole reading. Below it, the evidence
// of the reading is discounted in proportion: an instruction is known by
// how it begins, and if that is in doubt, so is everything read after it.
const anchorQuality = 0.8

// anchorCredibility returns the factor by which the evidence of a reading
// is discounted for the quality of its first required word.
func anchorCredibility(anchor float32) float32 {
	if anchor == 0 || anchor >= anchorQuality {
		return 1
	}
	return max(0, (anchor-similarityFloor)/(anchorQuality-similarityFloor))
}

// match is one reading of the tokens from a starting position.
type match struct {
	end      int     // position after the last token of the reading
	evidence float32 // evidence for the reading
	values   []any   // captured values, in template order
	// anchor is how closely the transcript matches the first required
	// word of the reading, or 0 if it has none.
	anchor float32
}

// pattern is a piece of a template.
type pattern interface {
	// matches returns the readings of the tokens at pos, at most one for
	// each end position.
	matches(d *decoding, pos int) []match
	// captures returns how many values a reading holds.
	captures() int
	// optional reports whether the pattern can match no tokens.
	optional() bool
}

// vocabWord is a word the controller can say.
type vocabWord struct {
	text   string
	sounds pronunciation
	weight float32
}

func makeVocabWord(text string) vocabWord {
	pr := pronouncePhrase(text)
	return vocabWord{text: text, sounds: pr, weight: pr.weight()}
}

// phraseWord is one position in a phrase: alternative words, one of which
// is said there.
type phraseWord struct {
	alternatives []vocabWord
	optional     bool
}

// phrase is a run of adjacent template words. It is matched as a unit
// because the recognizer does not respect word boundaries: one transcript
// word can stand for two template words ("flighting" for "fly heading")
// and the reverse ("local id" for "localizer").
type phrase struct {
	words []phraseWord
	// targets[i] lists the word sequences that a single matching step can
	// read starting at word i: up to maxSpan words, with any optional
	// words among them either said or left out.
	targets [][]phraseTarget
}

// phraseTarget is a sequence of template words that one matching step
// reads together.
type phraseTarget struct {
	next  int // index of the word after the sequence
	words int // number of words in the sequence
	said  vocabWord
}

func newPhrase(words []phraseWord) *phrase {
	p := &phrase{words: words, targets: make([][]phraseTarget, len(words))}
	for i := range words {
		// Enumerate the sequences that start with word i being said.
		var extend func(next int, said []vocabWord)
		extend = func(next int, said []vocabWord) {
			if len(said) > 0 {
				joined := said[0]
				if len(said) > 1 {
					texts := make([]string, len(said))
					for j, w := range said {
						texts[j] = w.text
					}
					joined = makeVocabWord(strings.Join(texts, " "))
				}
				p.targets[i] = append(p.targets[i], phraseTarget{next: next, words: len(said), said: joined})
			}
			if len(said) == maxSpan || next == len(words) {
				return
			}
			// After the first word, an optional word can be left out.
			if len(said) > 0 && words[next].optional {
				extend(next+1, said)
			}
			for _, alt := range words[next].alternatives {
				extend(next+1, append(slices.Clone(said), alt))
			}
		}
		extend(i, nil)
	}
	return p
}

func (p *phrase) captures() int { return 0 }

func (p *phrase) optional() bool {
	return !slices.ContainsFunc(p.words, func(w phraseWord) bool { return !w.optional })
}

func (p *phrase) matches(d *decoding, pos int) []match {
	return d.memoized(p, pos, func() []match {
		// cells[w][t] is the best reading of words[:w] from
		// tokens[pos:pos+t].
		type cell struct {
			evidence float32
			anchor   float32
			reached  bool
			said     bool
		}
		nw := len(p.words)
		nt := min(len(d.tokens)-pos, maxSpan*nw+maxGap)
		cells := make([][]cell, nw+1)
		for w := range cells {
			cells[w] = make([]cell, nt+1)
		}
		cells[0][0].reached = true
		relax := func(w, t int, c cell) {
			if cur := &cells[w][t]; !cur.reached || c.evidence > cur.evidence {
				*cur = c
			}
		}

		for w := range nw {
			for t := 0; t <= nt; t++ {
				c := cells[w][t]
				if !c.reached {
					continue
				}
				if p.words[w].optional {
					relax(w+1, t, c)
				}
				// Noise between the words of a phrase, once it has started.
				gapEnd := t
				var gap float32
				for skipped := 0; ; {
					for _, target := range p.targets[w] {
						for span := 1; span <= maxSpan && gapEnd+span <= nt; span++ {
							// One step reads one word as several or several as
							// one, never several as several: each word of the
							// phrase must be heard.
							if span > 1 && target.words > 1 {
								break
							}
							q := d.spanSimilarity(pos+gapEnd, span, target.said)
							if q <= similarityFloor {
								continue
							}
							// A number word is that number, and a small word is
							// itself: neither is a word of the phraseology that
							// was misheard, unless the two sound the same.
							heard := d.tokens[pos+gapEnd : pos+gapEnd+span]
							if q < homophoneQuality && !slices.ContainsFunc(heard, func(t token) bool {
								return !isNumberWord(t.text) && !isFunctionWord(t.text)
							}) && confusionQuality(d.spanText(pos+gapEnd, pos+gapEnd+span), target.said.text) == 0 {
								continue
							}
							// One word stands for several only if it has sound
							// left over after the first of them.
							if target.words > 1 && !p.hasLeftover(d, pos+gapEnd, w, target) {
								continue
							}
							next := cell{evidence: c.evidence - gap + target.said.weight*support(q), anchor: c.anchor, reached: true, said: true}
							if next.anchor == 0 && p.required(w, target.next) {
								next.anchor = q
							}
							relax(target.next, gapEnd+span, next)
						}
					}
					// Noise is passed over only to reach a word that the
					// phrase needs.
					if !c.said || gapEnd >= nt || p.words[w].optional {
						break
					}
					tok := d.tokens[pos+gapEnd]
					if !isStopword(tok.text) {
						if skipped += d.gapWeight(pos + gapEnd); skipped > maxGap {
							break
						}
						gap += gapCost * tok.sounds.weight() * float32(d.gapWeight(pos+gapEnd))
					}
					gapEnd++
				}
			}
		}

		var ms []match
		for t := 0; t <= nt; t++ {
			if c := cells[nw][t]; c.reached && (c.said || t == 0) {
				ms = append(ms, match{end: pos + t, evidence: c.evidence, anchor: c.anchor})
			}
		}
		return ms
	})
}

// leftoverShare is the share of the sound of the later words of a run
// that a single transcript word must have, beyond the sound of the first
// word, to be read as the whole run.
const leftoverShare = 0.5

// hasLeftover reports whether the token at pos has enough sound to stand
// for the target, a run of words starting with word w of the phrase. A
// known hearing of the recognizer needs none.
func (p *phrase) hasLeftover(d *decoding, pos, w int, target phraseTarget) bool {
	if confusionQuality(d.tokens[pos].text, target.said.text) > 0 {
		return true
	}
	heard := d.tokens[pos].sounds.weight()
	for _, first := range p.words[w].alternatives {
		if strings.HasPrefix(target.said.text, first.text+" ") {
			return heard >= first.weight+leftoverShare*(target.said.weight-first.weight)
		}
	}
	return true
}

// required reports whether any of words[from:to] is not optional.
func (p *phrase) required(from, to int) bool {
	return slices.ContainsFunc(p.words[from:to], func(w phraseWord) bool { return !w.optional })
}

// sequence matches its elements in order. Noise words may come between
// one element and the next, at a cost.
type sequence struct {
	elements []pattern
}

func (s *sequence) captures() int {
	n := 0
	for _, e := range s.elements {
		n += e.captures()
	}
	return n
}

func (s *sequence) optional() bool {
	return !slices.ContainsFunc(s.elements, func(e pattern) bool { return !e.optional() })
}

func (s *sequence) matches(d *decoding, pos int) []match {
	return d.memoized(s, pos, func() []match {
		return bestPerEnd(s.from(d, 0, pos, false))
	})
}

// from returns the readings of elements[i:] at pos. started says whether
// an earlier element consumed tokens: only then may noise be skipped, so
// that a production always begins at its first word.
func (s *sequence) from(d *decoding, i, pos int, started bool) []match {
	if i == len(s.elements) {
		return []match{{end: pos}}
	}
	e := s.elements[i]

	var ms []match
	gapEnd := pos
	var gap float32
	for skipped := 0; ; {
		for _, m := range e.matches(d, gapEnd) {
			consumed := m.end > gapEnd
			if !consumed && gapEnd > pos {
				continue // noise is only skipped to reach something
			}
			for _, rest := range s.from(d, i+1, m.end, started || consumed) {
				anchor := m.anchor
				if anchor == 0 {
					anchor = rest.anchor
				}
				ms = append(ms, match{
					end:      rest.end,
					evidence: m.evidence - gap + rest.evidence,
					values:   slices.Concat(m.values, rest.values),
					anchor:   anchor,
				})
			}
		}
		if !started || gapEnd >= len(d.tokens) {
			break
		}
		tok := d.tokens[gapEnd]
		if !isStopword(tok.text) {
			if skipped += d.gapWeight(gapEnd); skipped > maxGap {
				break
			}
			gap += gapCost * tok.sounds.weight() * float32(d.gapWeight(gapEnd))
		}
		gapEnd++
	}
	return bestPerEnd(ms)
}

// bestPerEnd keeps the reading with the most evidence for each end
// position. Later elements depend only on where a reading ends, so no
// other reading can be part of the best overall interpretation.
func bestPerEnd(ms []match) []match {
	slices.SortStableFunc(ms, func(a, b match) int {
		if a.end != b.end {
			return a.end - b.end
		}
		if a.evidence > b.evidence {
			return -1
		} else if a.evidence < b.evidence {
			return 1
		}
		return 0
	})
	return slices.CompactFunc(ms, func(a, b match) bool { return a.end == b.end })
}

// optionalGroup matches its sequence or nothing. When it matches nothing
// its captures are nil.
type optionalGroup struct {
	inner pattern
}

func (o *optionalGroup) captures() int  { return o.inner.captures() }
func (o *optionalGroup) optional() bool { return true }

func (o *optionalGroup) matches(d *decoding, pos int) []match {
	ms := []match{{end: pos, values: make([]any, o.inner.captures())}}
	for _, m := range o.inner.matches(d, pos) {
		if m.end > pos {
			ms = append(ms, m)
		}
	}
	return ms
}

// slot captures a value of some type by its own means: a number, a fix, an
// approach.
type slot struct {
	name string
	read func(d *decoding, pos int) []match
}

func (s *slot) captures() int  { return 1 }
func (s *slot) optional() bool { return false }

func (s *slot) matches(d *decoding, pos int) []match {
	return d.memoized(s, pos, func() []match {
		ms := s.read(d, pos)
		// A value, "correction", and another value of the same kind is the
		// second value.
		for _, m := range slices.Clone(ms) {
			if d.text(m.end) != "correction" {
				continue
			}
			for _, corrected := range s.read(d, m.end+1) {
				// The first value was heard too, so it stays explained.
				corrected.evidence += m.evidence + correctionWord.weight
				ms = append(ms, corrected)
			}
		}
		return bestPerEnd(ms)
	})
}

// captured gives a production's function access to what its template
// matched: the values of its slots, in template order, and the tokens the
// reading covers.
type captured struct {
	values     []any
	d          *decoding
	start, end int
}

func (c captured) has(i int) bool   { return c.values[i] != nil }
func (c captured) int(i int) int    { return c.values[i].(int) }
func (c captured) str(i int) string { return c.values[i].(string) }

// said reports whether one of the words was said, exactly, in the tokens
// the reading covers.
func (c captured) said(words ...string) bool {
	return slices.ContainsFunc(c.d.tokens[c.start:c.end], func(t token) bool { return slices.Contains(words, t.text) })
}

// production is a template with the function that builds its result.
type production struct {
	name     string
	template string
	body     *sequence
	// build returns the production's value, or nil if the reading is not
	// acceptable.
	build func(c captured) any
	// scale multiplies the evidence of every reading of the production.
	scale float32
}

func (p *production) captures() int  { return 1 }
func (p *production) optional() bool { return false }

func (p *production) matches(d *decoding, pos int) []match {
	return d.memoized(p, pos, func() []match {
		var ms []match
		for _, m := range p.body.matches(d, pos) {
			if m.end == pos {
				continue
			}
			v := p.build(captured{values: m.values, d: d, start: pos, end: m.end})
			if v == nil {
				continue
			}
			ms = append(ms, match{end: m.end, evidence: m.evidence * p.scale * anchorCredibility(m.anchor), values: []any{v}})
		}
		return ms
	})
}

// nonterminal is a named set of alternative productions.
type nonterminal struct {
	name         string
	alternatives []*production
}

func (n *nonterminal) captures() int  { return 1 }
func (n *nonterminal) optional() bool { return false }

func (n *nonterminal) matches(d *decoding, pos int) []match {
	return d.memoized(n, pos, func() []match {
		var ms []match
		for _, alt := range n.alternatives {
			ms = append(ms, alt.matches(d, pos)...)
		}
		return bestPerEnd(ms)
	})
}

// grammar holds the slots and nonterminals that templates can refer to.
type grammar struct {
	slots        map[string]*slot
	nonterminals map[string]*nonterminal
	// vocabulary holds every word that appears in a template.
	vocabulary map[string]bool
}

func newGrammar() *grammar {
	return &grammar{
		slots:        make(map[string]*slot),
		nonterminals: make(map[string]*nonterminal),
		vocabulary:   make(map[string]bool),
	}
}

// slot registers a slot under a name.
func (g *grammar) slot(name string, read func(d *decoding, pos int) []match) {
	g.slots[name] = &slot{name: name, read: read}
}

// define adds a production to the nonterminal with the given name. Its
// template may refer to slots and to nonterminals that are already
// defined.
func (g *grammar) define(name, template string, build func(c captured) any) *production {
	body, err := g.compile(template)
	if err != nil {
		panic(fmt.Sprintf("stt: template %q: %v", template, err))
	}
	p := &production{name: name, template: template, body: body, build: build, scale: 1}
	nt, ok := g.nonterminals[name]
	if !ok {
		nt = &nonterminal{name: name}
		g.nonterminals[name] = nt
	}
	nt.alternatives = append(nt.alternatives, p)
	return p
}

// compile parses a template into a sequence.
func (g *grammar) compile(template string) (*sequence, error) {
	var elements []pattern
	var words []phraseWord
	flush := func() {
		if len(words) > 0 {
			elements = append(elements, newPhrase(words))
			words = nil
		}
	}
	makeWord := func(field string, optional bool) phraseWord {
		w := phraseWord{optional: optional}
		for alt := range strings.SplitSeq(field, "|") {
			w.alternatives = append(w.alternatives, makeVocabWord(alt))
			g.vocabulary[alt] = true
		}
		return w
	}

	for rest := strings.TrimSpace(template); rest != ""; rest = strings.TrimSpace(rest) {
		switch rest[0] {
		case '[':
			end := closingBracket(rest)
			if end < 0 {
				return nil, fmt.Errorf("unmatched '['")
			}
			inner := rest[1:end]
			rest = rest[end+1:]
			if strings.ContainsAny(inner, "{[") {
				flush()
				seq, err := g.compile(inner)
				if err != nil {
					return nil, err
				}
				elements = append(elements, &optionalGroup{inner: seq})
			} else {
				for field := range strings.FieldsSeq(inner) {
					words = append(words, makeWord(field, true))
				}
			}

		case '{':
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return nil, fmt.Errorf("unmatched '{'")
			}
			name := rest[1:end]
			rest = rest[end+1:]
			flush()
			if s, ok := g.slots[name]; ok {
				elements = append(elements, s)
			} else if nt, ok := g.nonterminals[name]; ok {
				elements = append(elements, nt)
			} else {
				return nil, fmt.Errorf("unknown slot or nonterminal %q", name)
			}

		default:
			end := strings.IndexAny(rest, " [{")
			if end < 0 {
				end = len(rest)
			}
			words = append(words, makeWord(rest[:end], false))
			rest = rest[end:]
		}
	}
	flush()
	return &sequence{elements: elements}, nil
}

// closingBracket returns the index of the ']' that closes the '[' at the
// start of s, or -1.
func closingBracket(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// functionWords are the small words that hold a sentence together. They
// carry no instruction on their own, so they may be skipped inside a
// production at no cost, and they are too short and too alike to be
// matched by resemblance.
var functionWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "of": true, "to": true, "for": true, "at": true,
	"in": true, "on": true, "or": true, "as": true, "is": true, "it": true, "its": true, "you": true, "your": true,
}

// hesitations are sounds and courtesies that say nothing.
var hesitations = map[string]bool{
	"uh": true, "um": true, "ah": true, "er": true, "eh": true, "please": true, "sir": true, "now": true,
}

func isFunctionWord(text string) bool { return functionWords[text] }

// isStopword reports whether a word can be passed over at no cost.
func isStopword(text string) bool { return functionWords[text] || hesitations[text] }
