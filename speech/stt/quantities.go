// speech/stt/quantities.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
)

// This file interprets digit strings as the quantities of the
// phraseology. Each quantity has a set of values the controller can mean:
// headings are 1 to 360 and nearly always a multiple of 5, speeds are 100
// to 400 knots in tens, altitudes are hundreds of feet. A digit string
// that is not such a value is mapped to the nearest one by undoing what
// the recognizer does to numbers: it drops digits, repeats them, and runs
// a following word into them.

// interpretHeading reads a digit string as a heading, which is spoken as
// three digits.
func interpretHeading(r numberReading) (quantity, bool) {
	s, v := r.digits, r.value()
	valid := func(h int) bool { return h >= 1 && h <= 360 }

	switch {
	case len(s) == 3 && valid(v):
		// Controllers assign headings in multiples of five.
		if v%5 != 0 {
			return quantity{v, qualityUnlikely}, true
		}
		return quantity{v, qualityExact}, true

	case len(s) == 3:
		// More than 360: the last digit is not part of the heading.
		if h := v / 10; valid(h) {
			if h%5 == 0 {
				return quantity{h, qualityExact}, true
			}
			return quantity{h, qualityDroppedLast}, true
		}

	case len(s) == 4:
		// One digit too many. Prefer the removal that leaves a heading the
		// controller would assign.
		var best quantity
		consider := func(digits string, quality float32) {
			h, _ := strconv.Atoi(digits)
			if !valid(h) {
				return
			}
			if h%5 != 0 {
				quality = qualityDroppedLast * quality
			}
			if quality > best.quality {
				best = quantity{h, quality}
			}
		}
		consider(s[:3], qualityExact)
		for i := 0; i+1 < len(s); i++ {
			if s[i] == s[i+1] {
				consider(s[:i]+s[i+1:], qualityStutter)
			}
		}
		consider(s[1:], qualityRestored)
		return best, best.quality > 0

	case len(s) == 2 && s[0] == '0':
		// "zero niner": the trailing zero was dropped.
		if v > 0 {
			return quantity{v * 10, qualityExact}, true
		}

	case len(s) == 2:
		// "two seven" is 270 with its zero dropped, but "four five" is 045.
		if v <= 36 && v%10 != 0 {
			return quantity{v * 10, qualityExact}, true
		}
		return quantity{v, qualityExact}, true

	case len(s) == 1 && v > 0:
		// One digit of the three.
		return quantity{v * 10, qualityUnlikely}, true
	}
	return quantity{}, false
}

// minimumSpeed returns the lowest speed the aircraft's type can fly, or 0
// if it is not known.
func (ac *Aircraft) minimumSpeed() int {
	if ac.AircraftType == "" || db.DB == nil {
		return 0
	}
	if perf, ok := db.DB.AircraftPerformance[ac.AircraftType]; ok {
		return int(perf.Speed.Min)
	}
	return 0
}

// interpretSpeed returns the function that reads a digit string as an
// assigned speed for the decoding's aircraft. Speeds are spoken as three
// digits and assigned in tens of knots.
func (d *decoding) interpretSpeed(r numberReading) (quantity, bool) {
	s, v := r.digits, r.value()
	valid := func(spd int) bool { return spd >= 100 && spd <= 400 }
	if s[0] == '0' {
		return quantity{}, false
	}

	var in quantity
	switch {
	case len(s) == 3 && valid(v):
		switch {
		case v%10 == 0:
			in = quantity{v, qualityExact}
		case v%100 >= 11 && v%100 <= 19:
			// "one eighteen" is "one eighty".
			in = quantity{v/100*100 + v%10*10, qualityRestored}
		default:
			in = quantity{v / 10 * 10, qualityRounded}
		}

	case len(s) == 4:
		// One digit too many: either the first or the last is not part of
		// the speed.
		first, _ := strconv.Atoi(s[:3])
		last, _ := strconv.Atoi(s[1:])
		switch {
		case valid(first) && valid(last):
			if last%10 == 0 && first%10 != 0 {
				in = quantity{last, qualityRestored}
			} else {
				in = quantity{first / 10 * 10, qualityRestored}
			}
		case valid(last):
			in = quantity{last / 10 * 10, qualityDroppedFirst}
		case valid(first):
			in = quantity{first / 10 * 10, qualityDroppedLast}
		default:
			return quantity{}, false
		}

	case len(s) >= 5:
		first, _ := strconv.Atoi(s[:3])
		if !valid(first) || first%10 != 0 {
			return quantity{}, false
		}
		in = quantity{first, qualityDroppedLast}

	case len(s) == 2:
		// The trailing zero was dropped. Slow speeds are rare, so "one four"
		// is more likely a garble than 140.
		if v*10 < 150 || !valid(v*10) {
			return quantity{}, false
		}
		in = quantity{v * 10, qualityDroppedLast}
		if d.text(r.end) == "knots" {
			in.quality = qualityRestored
		}

	default:
		return quantity{}, false
	}

	// No more than 250 knots is assigned below 10,000 feet.
	if alt := d.aircraft.Altitude; in.value > 250 && alt > 0 && alt < 10000 {
		in.quality *= qualityUnlikely
	}

	// A speed below what the aircraft can fly had its first digit misheard.
	if lowest := d.aircraft.minimumSpeed(); lowest > 0 && in.value < lowest {
		for spd := in.value + 100; spd <= 400; spd += 100 {
			if spd < lowest || (spd > 250 && d.aircraft.Altitude > 0 && d.aircraft.Altitude < 10000) {
				continue
			}
			return quantity{spd, in.quality * qualityMisheardDigit}, true
		}
	}
	return in, true
}

// interpretMach reads a digit string as a Mach number in hundredths.
func interpretMach(r numberReading) (quantity, bool) {
	switch v := r.value(); {
	case len(r.digits) == 2 && v >= 60:
		return quantity{v, qualityExact}, true
	case len(r.digits) == 1 && v >= 6:
		return quantity{v * 10, qualityRestored}, true
	}
	return quantity{}, false
}

// interpretRange returns a function that accepts a number between lo and
// hi as it was spoken.
func interpretRange(lo, hi int) func(r numberReading) (quantity, bool) {
	return func(r numberReading) (quantity, bool) {
		v := r.value()
		if v < lo || v > hi || (len(r.digits) > 1 && r.digits[0] == '0' && lo > 0) {
			return quantity{}, false
		}
		return quantity{v, qualityExact}, true
	}
}

// A speed that holds until a distance on final ends between these
// distances, in miles from the runway. A speed is not assigned inside the
// lowest (7110.65 5-7-1).
const (
	lowestFinalMiles  = 5
	highestFinalMiles = 20
)

// readVagueFinalMiles reads one word that is not a number as the distance
// in "until five mile final". The words around it say that a distance was
// said, so the word is the distance it sounds most like, however faintly.
// When no distance stands out from the others, it is the lowest one,
// which is the one that controllers use most.
func (d *decoding) readVagueFinalMiles(pos int) []match {
	if pos >= len(d.tokens) || d.tokens[pos].numeral || isStopword(d.tokens[pos].text) || len(d.atomsAt(pos)) > 0 {
		return nil
	}
	const least, margin = 0.4, 0.1
	var best, usual numberWord
	var bestQuality, second, usualQuality float32
	for _, w := range numberVocabulary {
		v, _ := strconv.Atoi(w.digits)
		if v < lowestFinalMiles || v > highestFinalMiles {
			continue
		}
		q := soundSimilarity(d.tokens[pos].sounds, w.sounds)
		if v == lowestFinalMiles && q > usualQuality {
			usual, usualQuality = w, q
		}
		if q > bestQuality {
			best, bestQuality, second = w, q, bestQuality
		} else if q > second {
			second = q
		}
	}
	if bestQuality-second < margin {
		best, bestQuality = usual, usualQuality
	}
	if bestQuality < least {
		return nil
	}
	v, _ := strconv.Atoi(best.digits)
	return []match{{end: pos + 1, evidence: best.weight * gain(bestQuality), values: []any{v}}}
}

// Altitudes are encoded in hundreds of feet: 8,000 feet is 80 and flight
// level 350 is 350.
const (
	lowestAltitude  = 10
	highestAltitude = 600
)

// readAltitude reads the words at pos as an altitude. Altitudes below
// 18,000 feet are spoken as thousands and hundreds; the flight levels are
// spoken as three digits. flightLevels says whether three bare digits can
// be an altitude here; where a speed is as likely, they are not.
func (d *decoding) readAltitude(pos int, flightLevels bool) []match {
	ms := d.readThousands(pos, flightLevels)
	for _, r := range d.readNumbers(pos) {
		if _, _, ok := d.thousandAfter(r.end); ok {
			continue
		}
		if in, ok := d.interpretBareAltitude(r, pos, flightLevels); ok {
			ms = append(ms, match{end: r.end, evidence: r.evidence * in.quality, values: []any{in.value}})
		}
	}
	return ms
}

// readThousands reads the words at pos as an altitude that is said in
// full, with the word "thousand": "one two thousand", "two thousand five
// hundred". levels is as for interpretThousands.
func (d *decoding) readThousands(pos int, levels bool) []match {
	var ms []match
	for _, r := range d.readNumbers(pos) {
		end, evidence, ok := d.thousandAfter(r.end)
		if !ok {
			// Feet written out as one numeral: "4000".
			if v := r.value(); r.atoms == 1 && len(r.digits) >= 4 && r.digits[0] != '0' && v <= 60000 && v%100 == 0 {
				ms = append(ms, match{end: r.end, evidence: r.evidence, values: []any{v / 100}})
			}
			continue
		}
		thousands, quality, digits, ok := interpretThousands(r, levels)
		if !ok {
			continue
		}
		evidence += digits
		m := match{end: end, evidence: evidence * quality, values: []any{thousands * 10}}
		ms = append(ms, m)
		for _, h := range d.readNumbers(end) {
			hundredEnd, hundredEvidence, ok := d.unitAt(h.end, atomHundred)
			if ok && len(h.digits) == 1 && h.digits != "0" && thousands <= 60 {
				ms = append(ms, match{end: hundredEnd, evidence: m.evidence + h.evidence + hundredEvidence,
					values: []any{thousands*10 + h.value()}})
			}
		}
	}
	return ms
}

// thousandAfter looks for the word "thousand" at pos, or after one
// hesitation or unintelligible word ("nine or thousand").
func (d *decoding) thousandAfter(pos int) (end int, evidence float32, ok bool) {
	if end, evidence, ok = d.unitAt(pos, atomThousand); ok {
		return
	}
	if pos < len(d.tokens) && (isStopword(d.tokens[pos].text) || d.isPlainWord(pos)) {
		if end, evidence, ok = d.unitAt(pos+1, atomThousand); ok {
			if !isStopword(d.tokens[pos].text) {
				evidence -= gapCost * d.tokens[pos].sounds.weight()
			}
			return
		}
	}
	return 0, 0, false
}

// highestThousands is the highest altitude that is spoken as a count of
// thousands of feet. Above it are the flight levels, which are spoken as
// three digits after "flight level".
const highestThousands = 17

// interpretThousands reads the digits before "thousand" as a count of
// thousands of feet. evidence is the evidence for the digits of the count.
// Number words ahead of the count belong to something else: the count is
// the most of the last words that makes one. A count is never said with a
// tens word, so in "seventy six thousand" it is six. levels says whether a
// count that is a flight level is as likely as any other; it is where
// "climb" or "descend" says that an altitude follows.
func interpretThousands(r numberReading, levels bool) (thousands int, quality, evidence float32, ok bool) {
	if s := r.digits; r.unitEvidence > 0 {
		return int(s[len(s)-1] - '0'), qualityDroppedFirst, r.unitEvidence, true
	}
	for _, e := range r.groupEvidence {
		evidence += e
	}
	for i := range r.groups {
		count := strings.Join(r.groups[i:], "")
		thousands, _ = strconv.Atoi(count)
		switch {
		case i == 0 && len(count) == 4 && count[:2] == count[2:] && thousands%100 >= 10 && thousands%100 <= highestThousands:
			// "one two, twelve thousand": the number was said twice.
			return thousands % 100, qualityStutter, evidence, true
		case len(count) <= 2 && thousands >= 1 && thousands <= highestAltitude/10 && (i == 0 || count[0] != '0'):
			quality = qualityExact
			if i > 0 {
				quality = qualityDroppedFirst
			}
			if thousands > highestThousands && !levels {
				quality *= qualityUnlikely
			}
			return thousands, quality, evidence, true
		}
		evidence -= r.groupEvidence[i]
	}
	return 0, 0, 0, false
}

// interpretBareAltitude reads a digit string with no "thousand" after it
// as an altitude.
func (d *decoding) interpretBareAltitude(r numberReading, pos int, flightLevels bool) (quantity, bool) {
	s, v := r.digits, r.value()
	switch {
	case len(s) >= 4 && s[0] != '0':
		// Feet: "8000", "12000".
		if v >= 1000 && v <= 60000 && v%100 == 0 {
			return quantity{v / 100, qualityExact}, true
		}
		// "one three, thirteen": the number was said twice.
		if len(s) == 4 && s[:2] == s[2:] && v%100 >= 10 && v%100 <= 60 {
			return quantity{v % 100 * 10, qualityStutter}, true
		}

	case len(s) == 4:
		// A leading zero marks a flight level: "zero two eight zero".
		if v >= lowestAltitude && v <= highestAltitude {
			return quantity{v, qualityExact}, true
		}

	case len(s) == 3:
		if v < 100 || v > highestAltitude {
			return quantity{}, false
		}
		// Three digits from 100 to 400 are a speed unless something says
		// otherwise.
		if !flightLevels && v <= 400 && s[0] != '0' {
			return quantity{}, false
		}
		if flightLevels && v%10 != 0 {
			// "one one five": the last word was "thousand".
			return quantity{v / 10 * 10, qualityRounded}, true
		}
		return quantity{v, qualityExact}, true

	case len(s) == 2 && v >= lowestAltitude:
		// "one six" for sixteen thousand, when the aircraft is above 1,600
		// feet. Two digits that are not a count of thousands are hundreds of
		// feet, which are not said this way.
		switch alt := d.aircraft.Altitude; {
		case v > highestThousands:
			return quantity{v, qualityUnlikely}, true
		case alt > 0 && v < alt/100:
			return quantity{v * 10, qualityRestored}, true
		}
		return quantity{v, qualityExact}, true

	case len(s) == 1 && v > 0:
		// "descend five": thousands. After "level" a lone digit is the
		// start of a flight level that was not understood.
		if prev := d.text(pos - 1); prev == "level" || prev == "fl" {
			return quantity{}, false
		}
		return quantity{v * 10, qualityRestored}, true
	}
	return quantity{}, false
}

// interpretFlightLevel reads the digits after "flight level".
func interpretFlightLevel(r numberReading) (quantity, bool) {
	switch v := r.value(); {
	case len(r.digits) == 3 && v >= lowestAltitude && v <= highestAltitude:
		return quantity{v, qualityExact}, true
	case len(r.digits) == 2 && v >= 10 && v <= 60:
		return quantity{v * 10, qualityRestored}, true
	}
	return quantity{}, false
}

// readSquawk reads four digits as a transponder code.
func (d *decoding) readSquawk(pos int) []match {
	var ms []match
	for _, r := range d.readNumbers(pos) {
		if len(r.digits) == 4 && r.value() <= 7777 {
			ms = append(ms, match{end: r.end, evidence: r.evidence, values: []any{r.digits}})
		}
	}
	return ms
}

// readFrequency reads "one two eight point eight" as a radio frequency.
// Digits around "point" that do not make a frequency are read as one that
// was not understood, with the value 0.
func (d *decoding) readFrequency(pos int) []match {
	var ms []match
	for _, whole := range d.readNumbers(pos) {
		if t := d.text(whole.end); t != "point" && t != "decimal" {
			continue
		}
		for _, frac := range d.readNumbers(whole.end + 1) {
			var khz int
			switch {
			case len(whole.digits) != 3 || len(frac.digits) > 2:
			case len(frac.digits) == 2:
				khz = whole.value()*1000 + frac.value()*10
			default:
				khz = whole.value()*1000 + frac.value()*100
			}
			ms = append(ms, match{end: frac.end, evidence: whole.evidence + frac.evidence,
				values: []any{av.Frequency(khz)}})
		}
	}
	return ms
}

// readAltimeterSetting reads four digits that are an altimeter setting in
// inches of mercury: 28.00 to 31.50.
func (d *decoding) readAltimeterSetting(pos int) []match {
	var ms []match
	for _, r := range d.readNumbers(pos) {
		if v := r.value(); len(r.digits) == 4 && v >= 2800 && v <= 3150 {
			ms = append(ms, match{end: r.end, evidence: r.evidence, values: []any{""}})
		}
	}
	return ms
}

// readAltimeter reads an altimeter setting: four digits, possibly with
// "point" after the second. The pilot does not act on it, so a word
// among the digits that is not a number is taken for a garbled digit.
func (d *decoding) readAltimeter(pos int) []match {
	digits, numbers := 0, 0
	var evidence float32
	end := pos
	for end < len(d.tokens) && digits < 4 {
		tok := d.tokens[end]
		switch atoms := d.atomsAt(end); {
		case digits == 2 && (tok.text == "point" || tok.text == "decimal"):
		case tok.numeral:
			digits += len(tok.text)
			numbers++
			evidence += tok.sounds.weight()
		case len(atoms) > 0 && atoms[0].kind != atomHundred && atoms[0].kind != atomThousand:
			digits += len(atoms[0].digits)
			numbers++
			evidence += atoms[0].evidence
		default:
			digits++
		}
		end++
	}
	if numbers < 2 && !(numbers == 1 && digits >= 4) {
		return nil
	}
	return []match{{end: end, evidence: evidence, values: []any{""}}}
}
