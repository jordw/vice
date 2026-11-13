// speech/stt/traffic.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"strconv"
	"strings"
)

// This file reads traffic advisories: "traffic, ten o'clock, five miles,
// southeastbound, heavy Boeing triple seven, four thousand". The clock
// position and the distance come first. What follows is a description
// that the pilot needs only to recognize the traffic, and somewhere in it
// the altitude. The description is free text, so it is skipped at a
// small cost and the altitude is whatever in it reads as one.

const (
	// trafficLookahead is how far past the previous item the clock
	// position and the distance are looked for.
	trafficLookahead = 4
	// descriptionLength is how many words of description may precede the
	// altitude.
	descriptionLength = 14
	// descriptionCost is the evidence lost, per unit of sound, for a word
	// of the description.
	descriptionCost = 0.03
)

var (
	oclockWord = makeVocabWord("o'clock")
	milesWords = []vocabWord{makeVocabWord("miles"), makeVocabWord("mile")}
	unknownAlt = []vocabWord{makeVocabWord("altitude unknown"), makeVocabWord("unknown altitude")}
)

// aircraftTypeNumbers are numbers that name an aircraft type and so are
// not an altitude.
var aircraftTypeNumbers = map[string]bool{
	"737": true, "738": true, "739": true, "757": true, "747": true, "767": true, "777": true, "787": true,
	"319": true, "320": true, "321": true, "330": true, "340": true, "350": true, "380": true, "788": true,
	"789": true, "748": true, "359": true,
}

// readTraffic reads a traffic advisory after the word "traffic" and
// returns its command.
func (d *decoding) readTraffic(pos int) []match {
	var ms []match
	for p := pos; p < len(d.tokens) && p <= pos+trafficLookahead; p++ {
		for _, clock := range d.readNumbers(p) {
			end, evidence := clock.end, clock.evidence
			oclock := clock.value()
			said := false
			if q := d.spanSimilarity(end, 1, oclockWord); q > similarityFloor {
				said = true
				evidence += oclockWord.weight * gain(q)
				end++
			}
			if oclock < 1 || oclock > 12 {
				// "Eight eleven o'clock": a word ran into the number.
				if !said {
					continue
				}
				if v := oclock % 100; v >= 1 && v <= 12 {
					oclock = v
				} else if v := oclock % 10; v >= 1 {
					oclock = v
				} else {
					continue
				}
			}
			ms = append(ms, d.readTrafficDistance(end, oclock, evidence)...)
		}
		if len(ms) > 0 || len(d.atomsAt(p)) > 0 {
			break
		}
	}
	return ms
}

// readTrafficDistance reads the distance and what follows it.
func (d *decoding) readTrafficDistance(pos, oclock int, evidence float32) []match {
	var ms []match
	for p := pos; p < len(d.tokens) && p <= pos+trafficLookahead; p++ {
		for _, miles := range d.readNumbers(p) {
			if v := miles.value(); v < 1 || v > 50 {
				continue
			}
			end, ev := miles.end, evidence+miles.evidence
			for _, w := range milesWords {
				if q := d.spanSimilarity(end, 1, w); q > similarityFloor {
					ev += w.weight * gain(q)
					end++
					break
				}
			}
			distance := miles.value()
			// "Nine miles, correction, seven miles."
			if d.text(end) == "correction" {
				for _, corrected := range d.readNumbers(end + 1) {
					if v := corrected.value(); v >= 1 && v <= 50 {
						distance, end, ev = v, corrected.end, ev+corrected.evidence
						if t := d.text(end); t == "miles" || t == "mile" {
							end++
						}
						break
					}
				}
			}
			ms = append(ms, d.readTrafficAltitude(end, oclock, distance, ev)...)
		}
		if len(ms) > 0 || len(d.atomsAt(p)) > 0 {
			break
		}
	}
	return ms
}

// readTrafficAltitude reads the description of the traffic for its
// altitude. It returns a reading that ends before the description, with
// the altitude unknown, and one for each altitude it finds.
func (d *decoding) readTrafficAltitude(pos, oclock, miles int, evidence float32) []match {
	command := func(alt string) []any { return []any{fmt.Sprintf("TRAFFIC/%d/%d/%s", oclock, miles, alt)} }
	ms := []match{{end: pos, evidence: evidence, values: command("UNK")}}
	// found adds a reading that ends with the altitude, and with what is
	// said about the traffic after it.
	found := func(end int, evidence float32, alt string) {
		end, more := d.readTrafficTail(end)
		ms = append(ms, match{end: end, evidence: evidence + more, values: command(alt)})
	}

	inSight := d.phraseology.nonterminals["other_has_us"]
	// understood is the evidence for the words of the description that
	// were recognized as describing the traffic, and cost the evidence
	// lost for those that were not.
	var understood, cost float32
	for p := pos; p < len(d.tokens) && p < pos+descriptionLength; p++ {
		// The number of an aircraft type is not an altitude, nor is any
		// part of it.
		if end := d.aircraftTypeNumberEnd(p); end > p {
			for _, tok := range d.tokens[p:end] {
				understood += tok.sounds.weight()
			}
			p = end - 1
			continue
		}
		ev := evidence + understood - cost
		if p > pos && d.text(p-1) == "at" {
			// "At three thousand": the word that introduces the altitude.
			ev += d.tokens[p-1].sounds.weight()
		}
		for _, w := range unknownAlt {
			if q := d.spanSimilarity(p, 2, w); q > similarityFloor {
				ms = append(ms, match{end: p + 2, evidence: ev + w.weight*gain(q), values: command("UNK")})
			}
		}
		for _, m := range inSight.matches(d, p) {
			ms = append(ms, match{end: m.end, evidence: ev + m.evidence, values: []any{"TRAFFIC/INSIGHT"}})
		}
		for _, m := range d.readThousands(p, true) {
			if alt := m.values[0].(int); alt <= highestAltitude {
				found(m.end, ev+m.evidence, strconv.Itoa(alt))
			}
		}
		for _, r := range d.readNumbers(p) {
			v := r.value()
			switch {
			case len(r.digits) >= 3 && v >= 500 && v <= 60000 && v%100 == 0 && r.digits[0] != '0':
				// Feet, as a numeral or as "five hundred".
				found(r.end, ev+r.evidence, strconv.Itoa(v/100))
			}
		}
		if d.text(p) == "flight" || d.text(p) == "fl" {
			for _, m := range d.phraseology.nonterminals["altitude_fl"].matches(d, p) {
				found(m.end, ev+m.evidence, strconv.Itoa(m.values[0].(int)))
			}
		}
		switch tok := d.tokens[p]; {
		case isStopword(tok.text):
		case trafficWords[tok.text] || strings.HasSuffix(tok.text, "bound"):
			understood += tok.sounds.weight()
		default:
			cost += descriptionCost * tok.sounds.weight()
		}
	}
	return ms
}

// trafficMotion are the words that say what the traffic's altitude is
// doing.
var trafficMotion = map[string]bool{"climbing": true, "descending": true, "level": true}

// readTrafficTail reads what is said about the traffic after its
// altitude: what the altitude is doing, the altitude said again, and the
// request to report the traffic in sight. It returns the position after
// those words and the evidence for them.
func (d *decoding) readTrafficTail(pos int) (end int, evidence float32) {
	end = pos
	for end < len(d.tokens) {
		tok := d.tokens[end]
		if trafficMotion[tok.text] {
			evidence += tok.sounds.weight()
			end++
			continue
		}
		// An altitude that is climbing or descending is the traffic's.
		restated := false
		for _, m := range d.readThousands(end, true) {
			if trafficMotion[d.text(m.end)] {
				end, evidence, restated = m.end, evidence+m.evidence, true
				break
			}
		}
		if !restated {
			break
		}
	}
	for _, m := range d.phraseology.nonterminals["traffic_report"].matches(d, end) {
		if m.evidence > 0 {
			return m.end, evidence + m.evidence
		}
	}
	return end, evidence
}

// trafficWords are words that describe traffic: its type and what it is
// doing.
var trafficWords = map[string]bool{
	"heavy": true, "super": true, "boeing": true, "airbus": true, "embraer": true, "cessna": true, "regional": true,
	"jet": true, "climbing": true, "descending": true, "level": true, "landing": true, "parallel": true,
	"runway": true, "type": true, "unknown": true, "altitude": true, "indicates": true, "triple": true,
}

// aircraftTypeNumberEnd returns the position after the number of an
// aircraft type that starts at pos ("seven thirty seven"), or pos if
// there is none.
func (d *decoding) aircraftTypeNumberEnd(pos int) int {
	end := pos
	for _, r := range d.readNumbers(pos) {
		if aircraftTypeNumbers[r.digits] {
			end = max(end, r.end)
		}
	}
	return end
}

// readTrafficInSight reads an advisory that gives the traffic's position
// in words ("off your left") and says that it has this aircraft in
// sight.
func (d *decoding) readTrafficInSight(pos int) []match {
	inSight := d.phraseology.nonterminals["other_has_us"]
	var cost float32
	for p := pos; p < len(d.tokens) && p < pos+descriptionLength; p++ {
		for _, m := range inSight.matches(d, p) {
			return []match{{end: m.end, evidence: m.evidence - cost, values: []any{""}}}
		}
		if tok := d.tokens[p]; !isStopword(tok.text) {
			cost += descriptionCost * tok.sounds.weight()
		}
	}
	return nil
}
