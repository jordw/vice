// speech/stt/unit_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"slices"
	"testing"

	"github.com/mmp/vice/sim"
)

func TestTokenize(t *testing.T) {
	for _, tt := range []struct {
		transcript string
		expected   []string
	}{
		{"American 123", []string{"american", "123"}},
		{"turn left, disregard. Turn right", []string{"turn", "left", "disregard", "turn", "right"}},
		{"1-1-thousand", []string{"1", "1", "thousand"}},
		{"@ cameron descend", []string{"at", "cameron", "descend"}},
		{"alaska8383", []string{"alaska", "8383"}},
		{"runway 22L", []string{"runway", "22", "left"}},
		{"Lufthansa 4WJ", []string{"lufthansa", "4", "w", "j"}},
		{"tower 118.7", []string{"tower", "118", "point", "7"}},
		{"eleven o'clock", []string{"eleven", "oclock"}},
		{"Allegéne two", []string{"allegene", "two"}},
		{"", nil},
		// A loop of the recognizer collapses...
		{"x ray x ray x ray jet", []string{"x", "ray", "jet"}},
		// ...but repeated digits are content, spelled out or not.
		{"november three three three mike echo", []string{"november", "three", "three", "three", "mike", "echo"}},
		{"november 3 3 3 mike echo", []string{"november", "3", "3", "3", "mike", "echo"}},
	} {
		if got := tokenTexts(tokenize(tt.transcript)); !slices.Equal(got, tt.expected) && len(got)+len(tt.expected) > 0 {
			t.Errorf("tokenize(%q) = %v, want %v", tt.transcript, got, tt.expected)
		}
	}
}

func TestSimilarity(t *testing.T) {
	for _, tt := range []struct {
		heard, target string
		least, most   float32
	}{
		{"american", "american", 1, 1},
		{"amurican", "american", 0.8, 1},
		{"deltta", "delta", 0.9, 1},
		{"tree", "three", 0.85, 1},
		{"won", "one", 0.85, 1},
		{"tepart", "depart", 0.85, 1},
		{"decend", "descend", 0.85, 1},
		{"flighting", "fly heading", 0.7, 1},
		{"safe", "say", 0.75, 1},
		// A known confusion that sound does not explain.
		{"tarom", "tower", 0.85, 0.85},
		// The small words are heard exactly or not at all.
		{"in", "at", 0, 0},
		{"cat", "dog", 0, 0.55},
		{"xyz", "depart", 0, 0.55},
		{"maximum", "minimum", 0, 0.75},
	} {
		if got := Similarity(tt.heard, tt.target); got < tt.least || got > tt.most {
			t.Errorf("Similarity(%q, %q) = %.2f, want %.2f to %.2f", tt.heard, tt.target, got, tt.least, tt.most)
		}
	}
}

// longestNumber returns the digits of the reading of the transcript as a
// number that takes in the most words, preferring the best supported.
func longestNumber(transcript string) string {
	d := newDecoding(thePhraseology(), tokenize(transcript), nil)
	var best numberReading
	for _, r := range d.readNumbers(0) {
		if r.end > best.end || r.end == best.end && r.evidence > best.evidence {
			best = r
		}
	}
	return best.digits
}

func TestReadNumbers(t *testing.T) {
	for _, tt := range []struct{ transcript, expected string }{
		{"tree fower too", "342"},
		{"two seven zero", "270"},
		{"two seventy", "270"},
		{"seventy three ten", "7310"},
		{"30 11", "3011"},
		{"two hundred", "200"},
		{"zero two zero", "020"},
		// "Niner" written as "nine or".
		{"two nine or zero", "290"},
		// "One" heard as "and" between digits.
		{"two and zero", "210"},
		// A word between digits that is no number at all.
		{"one ah nine zero", "190"},
		// The second number replaces the first.
		{"two seven zero correction two niner zero", "290"},
		{"heading", ""},
	} {
		if got := longestNumber(tt.transcript); got != tt.expected {
			t.Errorf("number in %q = %q, want %q", tt.transcript, got, tt.expected)
		}
	}
}

func TestReadWords(t *testing.T) {
	for _, tt := range []struct {
		transcript string
		words      int
		kind       WordKind
		value      int
	}{
		{"8 thousand", 1, WordAltitude, 80},
		{"one two thousand five hundred", 1, WordAltitude, 125},
		{"2 7 0", 1, WordNumber, 270},
		{"heading 2 7 0", 2, WordPlain, 0},
	} {
		words := ReadWords(tt.transcript)
		if len(words) != tt.words || words[0].Kind != tt.kind || words[0].Value != tt.value {
			t.Errorf("ReadWords(%q) = %+v, want %d words starting with kind %d value %d", tt.transcript, words, tt.words,
				tt.kind, tt.value)
		}
	}
}

// decodeFor decodes the words as a transmission to a single aircraft and
// returns the commands.
func decodeFor(ac Aircraft, words string) string {
	ac.Callsign = "AAL123"
	out, _ := NewTranscriber(nil).DecodeCommandsForCallsign(map[string]Aircraft{"AAL123": ac}, words, "AAL123")
	return out
}

func TestQuantities(t *testing.T) {
	jet := Aircraft{Altitude: 10000, State: "arrival", AircraftType: "B738"}
	for _, tt := range []struct{ words, expected string }{
		// Flight levels.
		{"climb and maintain flight level three five zero", "C350"},
		{"climb and maintain flight level one minor zero", "C190"},
		{"climb and maintain flight level one landing zero", "SAYAGAIN/ALTITUDE"},
		// Squawk codes are four digits.
		{"squawk one two zero zero", "SQ1200"},
		{"squawk 7700", "SQ7700"},
		{"squawk one two", "AGAIN"},
		// A turn by degrees needs "degrees" or the number before the side.
		{"turn 20 degrees left", "T20L"},
		{"turn 30 right", "T30R"},
		{"turn left 15 degrees", "T15L"},
		{"turn left 15", "L150"},
		// Frequencies.
		{"contact tower 123 point 45", "TO/123450"},
		{"contact tower 118 point 9", "TO/118900"},
		{"contact tower 120 point 0", "TO/120000"},
		{"contact tower 118 point 09", "TO/118090"},
		{"contact tower 12 point 5", "TO"},
		// A distance and direction from a fix.
		{"cross one five miles southwest of rosly at one one thousand", "CROSLY/15SW/A110"},
	} {
		ac := jet
		ac.Fixes = map[string]string{"rosly": "ROSLY"}
		if got := decodeFor(ac, tt.words); got != tt.expected {
			t.Errorf("%q: got %q, want %q", tt.words, got, tt.expected)
		}
	}
}

// TestPlausibility checks the instructions that are rejected, or asked
// about, because the aircraft cannot do them.
func TestPlausibility(t *testing.T) {
	approaches := map[string]string{"ILS Runway 22L": "I22L"}
	for _, tt := range []struct {
		name     string
		ac       Aircraft
		words    string
		expected string
	}{
		{"descend below", Aircraft{Altitude: 12000, State: "arrival"}, "descend and maintain eight thousand", "D80"},
		{"descend to an altitude above", Aircraft{Altitude: 10000, State: "arrival"},
			"descend and maintain one five thousand", "SAYAGAIN/ALTITUDE"},
		{"climb above", Aircraft{Altitude: 5000, State: "departure"}, "climb and maintain one zero thousand", "C100"},
		{"climb to an altitude below", Aircraft{Altitude: 10000, State: "departure"},
			"climb and maintain five thousand", "SAYAGAIN/ALTITUDE"},
		{"expedite climb for an arrival", Aircraft{Altitude: 10000, State: "arrival"}, "expedite climb", "AGAIN"},
		{"expedite descent for a departure", Aircraft{Altitude: 10000, State: "departure"}, "expedite descent", "AGAIN"},
		{"expedite descent for a departure that lands here",
			Aircraft{Altitude: 10000, State: "departure", CandidateApproaches: approaches}, "expedite descent", "ED"},
		{"contact tower", Aircraft{State: "arrival"}, "contact tower", "TO"},
		{"climb via for an arrival", Aircraft{State: "arrival"}, "climb via the sid", "AGAIN"},
		{"climb via for a departure", Aircraft{State: "departure"}, "climb via the sid", "CVS"},
		{"climb via except above", Aircraft{State: "departure", Altitude: 3000},
			"climb via the sid except maintain one zero thousand", "CVS/A100"},
		{"climb via except below", Aircraft{State: "departure", Altitude: 5000},
			"climb via the sid except maintain two thousand", "SAYAGAIN/ALTITUDE"},
		{"descend via except below", Aircraft{State: "arrival", Altitude: 25000},
			"descend via the star except maintain one two thousand", "DVS/A120"},
		{"descend via except above", Aircraft{State: "arrival", Altitude: 12000},
			"descend via the star except maintain one five thousand", "SAYAGAIN/ALTITUDE"},
		{"altitude discretion for an arrival", Aircraft{State: "arrival"}, "altitude your discretion", "AGAIN"},
		{"altitude discretion for VFR", Aircraft{State: "vfr flight following"}, "altitude your discretion", "A"},
		{"heading survives a bad descent", Aircraft{Altitude: 10000, State: "arrival"},
			"turn left heading two seven zero descend and maintain one five thousand", "L270 SAYAGAIN/ALTITUDE"},
		{"speed limit", Aircraft{State: "arrival", AircraftType: "B738"}, "do not exceed two five zero knots", "S250-"},
		{"speed floor", Aircraft{State: "arrival", AircraftType: "B738"}, "maintain two five zero knots or greater", "S250+"},
		{"speed limit until", Aircraft{State: "arrival", AircraftType: "B738"},
			"maintain one eight zero knots or less until five dme", "S180-/U5DME"},
		// The count before "thousand" is one or two digits or one word.
		{"tens word before the count", Aircraft{Altitude: 12000, State: "arrival"}, "seventy six thousand", "A60"},
		{"number before the count", Aircraft{Altitude: 10000, State: "arrival"}, "seventeen five thousand", "A50"},
		{"flight level said as thousands", Aircraft{Altitude: 5000, State: "arrival"}, "maintain two four thousand", "A40"},
		{"three digits before thousand", Aircraft{Altitude: 21000, State: "arrival"},
			"descend one three four thousand", "SAYAGAIN/ALTITUDE"},
		{"number before knots is a speed", Aircraft{Altitude: 4000, State: "arrival"}, "maintain blark two knots", "SAYAGAIN/SPEED"},
		// A climb to an altitude below the aircraft, with a verb that was
		// not heard as such: the altitude is in doubt when the verb is all
		// that came before it, and the verb is when it came among words
		// that were not understood.
		{"misheard climb", Aircraft{Altitude: 6000, State: "arrival"}, "claimed five thousand", "SAYAGAIN/ALTITUDE"},
		{"misheard climb among garbled words", Aircraft{Altitude: 6000, State: "arrival"},
			"the cinema claimed five thousand", "A50"},
		// Headings are multiples of five, and are said as three digits.
		{"heading with a stray word", Aircraft{State: "arrival"}, "turn latin two zero zero and maintain three thousand", "H200 A30"},
		{"fly without heading", Aircraft{State: "departure", Altitude: 2500}, "flight in three six zero", "H360"},
		// No speed limit ends inside five miles from the runway.
		{"speed until four mile final", Aircraft{State: "arrival", AircraftType: "B738"},
			"maintain one eight zero knots until fire mile final", "S180/U5"},
		{"speed until a distance that was not understood", Aircraft{State: "arrival", AircraftType: "B738"},
			"speed one eight zero until final mile final", "S180/U5"},
		{"speed until, corrected", Aircraft{State: "arrival", AircraftType: "B738"},
			"speed one eight zero knots until eight miles correction seven miles", "S180/U7"},
		// A side after the runway number is the runway's.
		{"side that is not the expected approach's", Aircraft{State: "arrival", AssignedApproach: "ILS Runway 22R",
			CandidateApproaches: map[string]string{"ILS Runway 22L": "I2L", "ILS Runway 22R": "I2R"}},
			"cleared ils runway two two left", "CI2L"},
		{"runway number with one digit misheard", Aircraft{State: "arrival", AssignedApproach: "ILS Runway 31R",
			CandidateApproaches: map[string]string{"ILS Runway 31L": "I1L", "ILS Runway 31R": "I1R"}},
			"cleared ils runway two one left approach", "CI1L"},
		{"runway before the kind", Aircraft{State: "arrival",
			CandidateApproaches: map[string]string{"ILS Runway 4R": "I4R", "RNAV Z Runway 4R": "Z4R"}},
			"expect runway four right rnav zulu approach", "EZ4R"},
		{"abbreviation written in capitals", Aircraft{State: "arrival", AssignedApproach: "ILS Runway 10R",
			CandidateApproaches: map[string]string{"ILS Runway 10L": "I0L", "ILS Runway 10R": "I0R"}},
			"cleared the ALS one zero right approach", "CI0R"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeFor(tt.ac, tt.words); got != tt.expected {
				t.Errorf("%q: got %q, want %q", tt.words, got, tt.expected)
			}
		})
	}
}

// TestExpediteAltitude checks that an expedite given with an altitude
// comes after the altitude's command, which would otherwise cancel it, and
// that only "through" makes the altitude where the expedite ends.
func TestExpediteAltitude(t *testing.T) {
	arrival := Aircraft{Altitude: 8000, AssignedAltitude: 4000, State: "arrival"}
	level := Aircraft{Altitude: 6000, AssignedAltitude: 6000, State: "arrival"}
	departure := Aircraft{Altitude: 5000, State: "departure"}
	for _, tt := range []struct {
		name     string
		ac       Aircraft
		words    string
		expected string
	}{
		{"to an altitude", arrival, "expedite to four thousand", "D40 ED"},
		{"altitude alone", arrival, "expedite four thousand", "D40 ED"},
		{"down to an altitude", arrival, "expedite down to four thousand", "D40 ED"},
		{"descent to an altitude", arrival, "expedite descent to four thousand", "D40 ED"},
		{"your descent to an altitude", arrival, "expedite your descent to four thousand", "D40 ED"},
		{"descent and an altitude", arrival, "expedite descent four thousand", "D40 ED"},
		{"descent, then the altitude", arrival, "expedite descent descend and maintain four thousand", "D40 ED"},
		{"descent through an altitude", arrival, "expedite descent through six thousand", "ED60"},
		{"best rate to an altitude", arrival, "best rate descent to four thousand", "D40 ED"},
		{"from level flight", level, "expedite to four thousand", "D40 ED"},
		{"climb to an altitude", departure, "expedite climb to one zero thousand", "C100 EC"},
		{"up to an altitude", departure, "expedite up to one zero thousand", "C100 EC"},
		{"after maintaining an altitude above", departure, "maintain one zero thousand expedite", "A100 EC"},
		{"after maintaining an altitude below", arrival, "maintain four thousand expedite", "A40 ED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeFor(tt.ac, tt.words); got != tt.expected {
				t.Errorf("%q: got %q, want %q", tt.words, got, tt.expected)
			}
		})
	}
}

func TestFixNames(t *testing.T) {
	for _, tt := range []struct {
		name     string
		fixes    map[string]string
		words    string
		expected string
	}{
		// Where only a fix can be said, a word that faintly resembles one
		// fix and no other is that fix.
		{"faint name after cleared", map[string]string{"Biggy": "BIGGY", "Copes": "COPES", "Modena": "MXE"},
			"cleared up bergy climb and maintain one seven thousand", "DBIGGY C170"},
		{"faint name that two fixes resemble", map[string]string{"Vocus": "VOCUS", "Boston": "KBOS"},
			"maintain one seven zero knots until voci", "S170"},
		{"faint name of a fix that was named before", map[string]string{"Vocus": "VOCUS", "Boston": "KBOS"},
			"two miles from vocus maintain one seven zero knots until voci", "S170/UVOCUS"},
		// A word that sounds like a whole name is not another name cut
		// short.
		{"whole name over a name cut short", map[string]string{"Berei": "BEREI", "Bruin": "BRUWN"},
			"proceed direct brea", "DBEREI"},
		// An identifier that starts with consonants that cannot be said
		// together.
		{"identifier read as a word", map[string]string{"Shee": "KSHEE", "Cashen": "CASHN"},
			"turn left direct kishi", "LDKSHEE"},
		{"altitude at a fix without a verb", map[string]string{"Ladle": "LADLE"},
			"direct ladle at ladle six thousand", "DLADLE ALADLE/D60"},
		{"proceed direct written as one word", map[string]string{"Greki": "GREKI"},
			"procedure at greki descend and maintain six thousand", "DGREKI D60"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ac := Aircraft{State: "arrival", Altitude: 10000, AircraftType: "B738", Fixes: tt.fixes}
			if got := decodeFor(ac, tt.words); got != tt.expected {
				t.Errorf("%q: got %q, want %q", tt.words, got, tt.expected)
			}
		})
	}
}

func TestCoalesceAfterFixAltitudes(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "cross fix then descend",
			input:    []string{"CROSLY/A60", "D30"},
			expected: []string{"CROSLY/A60", "AROSLY/D30"},
		},
		{
			name:     "cross fix then climb",
			input:    []string{"CROSLY/A60", "C90"},
			expected: []string{"CROSLY/A60", "AROSLY/C90"},
		},
		{
			name:     "cross fix then maintain below is descend",
			input:    []string{"CROSLY/A60", "A50"},
			expected: []string{"CROSLY/A60", "AROSLY/D50"},
		},
		{
			name:     "cross fix then maintain above is climb",
			input:    []string{"CROSLY/A60", "A90"},
			expected: []string{"CROSLY/A60", "AROSLY/C90"},
		},
		{
			name:     "cross fix then then-descend",
			input:    []string{"CROSLY/A60", "TD30"},
			expected: []string{"CROSLY/A60", "AROSLY/D30"},
		},
		{
			name:     "standalone descend unchanged",
			input:    []string{"D30"},
			expected: []string{"D30"},
		},
		{
			name:     "cross fix then speed unchanged",
			input:    []string{"CROSLY/A60", "S210"},
			expected: []string{"CROSLY/A60", "S210"},
		},
		{
			name:     "cross fix then direct-to-fix unchanged",
			input:    []string{"CROSLY/A60", "DROSLY"},
			expected: []string{"CROSLY/A60", "DROSLY"},
		},
		{
			name:     "non-cross C command unchanged",
			input:    []string{"C90", "D30"},
			expected: []string{"C90", "D30"},
		},
		{
			name:     "cleared approach unchanged",
			input:    []string{"CI9L", "D30"},
			expected: []string{"CI9L", "D30"},
		},
		{
			name:     "cross fix at or above then descend",
			input:    []string{"CJOBAS/A57+", "D30"},
			expected: []string{"CJOBAS/A57+", "AJOBAS/D30"},
		},
		{
			name:     "cross fix at or below then climb",
			input:    []string{"CJOBAS/A57-", "C90"},
			expected: []string{"CJOBAS/A57-", "AJOBAS/C90"},
		},
		{
			name:     "only transforms immediately following command",
			input:    []string{"CROSLY/A60", "D30", "D20"},
			expected: []string{"CROSLY/A60", "AROSLY/D30", "D20"},
		},
		{
			name:     "multiple cross-fix commands",
			input:    []string{"CROSLY/A60", "D30", "CJOBAS/A57+", "D20"},
			expected: []string{"CROSLY/A60", "AROSLY/D30", "CJOBAS/A57+", "AJOBAS/D20"},
		},
		{
			name:     "single command unchanged",
			input:    []string{"CROSLY/A60"},
			expected: []string{"CROSLY/A60"},
		},
		{
			name:     "cross distance then descend unchanged",
			input:    []string{"CDETGY/5W/A30", "D20"},
			expected: []string{"CDETGY/5W/A30", "D20"},
		},
		{
			name:     "cross fix altitude and speed still coalesces",
			input:    []string{"CROSLY/A60/S250", "D20"},
			expected: []string{"CROSLY/A60/S250", "AROSLY/D20"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy so we don't modify test data
			input := make([]string, len(tt.input))
			copy(input, tt.input)
			got := compose(input, &Aircraft{})
			if len(got) != len(tt.expected) {
				t.Fatalf("got %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %v, want %v", got, tt.expected)
					break
				}
			}
		})
	}
}

func TestCallsignRepairs(t *testing.T) {
	aircraft := map[string]Aircraft{
		"JetBlue 40 95":                         {Callsign: "JBU4095", State: "arrival", Altitude: 9000},
		"JetBlue 87 67":                         {Callsign: "JBU8767", State: "arrival", Altitude: 9000},
		"KLM 31 romeo heavy":                    {Callsign: "KLM31R", State: "arrival", Altitude: 9000},
		"Speedbird six 48 heavy":                {Callsign: "BAW648", State: "arrival", Altitude: 9000},
		"Southwest 59 85":                       {Callsign: "SWA5985", State: "arrival", Altitude: 9000},
		"Air Canada 64 heavy":                   {Callsign: "ACA64", State: "arrival", Altitude: 9000},
		"november four eight two juliet yankee": {Callsign: "N482JY", State: "arrival", Altitude: 9000},
		"november niner niner one zero zulu":    {Callsign: "N9910Z", State: "arrival", Altitude: 9000},
	}
	for _, tt := range []struct{ transcript, expected string }{
		// "Fourteen" for "forty".
		{"JetBlue fourteen ninety five descend and maintain four thousand", "JBU4095 D40"},
		// A word inside the identifier that was not understood.
		{"KLM three mar romeo descend and maintain four thousand", "KLM31R D40"},
		// The only aircraft with its name, and an identifier that resembles
		// what was heard.
		{"Speedbird six five four descend and maintain four thousand", "BAW648 D40"},
		// The identifier alone, exact, after a name that was not understood.
		{"Hurricane Anavis sixty four heavy descend and maintain four thousand", "ACA64 D40"},
		// The identifier shortened to its last characters.
		{"two juliett yankee descend and maintain four thousand", "N482JY D40"},
		// "Zero" for "zulu".
		{"november one zero zero descend and maintain four thousand", "N9910Z D40"},
		// A resemblance alone, with a first word that may be a name, is not
		// enough.
		{"Fozo six nine six five descend and maintain four thousand", ""},
	} {
		if got, _ := NewTranscriber(nil).DecodeTranscript(aircraft, tt.transcript, ""); got != tt.expected {
			t.Errorf("%q: got %q, want %q", tt.transcript, got, tt.expected)
		}
	}
}

// The /T suffix is part of what a test file expects only if the transcript
// opens with the words that tell the two forms of address apart.
func TestTestFileMatchesAddressingForm(t *testing.T) {
	aircraft := map[string]Aircraft{
		"november one two three alpha bravo": {Callsign: "N123AB"},
		"skyhawk three alpha bravo":          {Callsign: "N123AB/T", AddressingForm: sim.AddressingFormTypeTrailing3},
	}
	for _, tt := range []struct {
		transcript, callsign, result string
		want                         bool
	}{
		{"November one two three alpha bravo fly heading zero nine zero", "N123AB", "N123AB H090", true},
		{"November one two three alpha bravo fly heading zero nine zero", "N123AB", "N123AB/T H090", false},
		{"Skyhawk three alpha bravo fly heading zero nine zero", "N123AB/T", "N123AB/T H090", true},
		{"Skyhawk three alpha bravo fly heading zero nine zero", "N123AB/T", "N123AB H090", false},
		{"Sky talk three alpha bravo fly heading zero nine zero", "N123AB/T", "N123AB H090", true},
		{"Sky talk three alpha bravo fly heading zero nine zero", "N123AB", "N123AB/T H090", true},
		{"Sky talk three alpha bravo fly heading zero nine zero", "N123AB", "N123AB/T H180", false},
	} {
		tf := TestFile{Transcript: tt.transcript, Callsign: tt.callsign, Command: "H090"}
		if got := tf.Matches(tt.result, aircraft); got != tt.want {
			t.Errorf("%q expecting %s, result %q: got %v, want %v", tt.transcript, tt.callsign, tt.result, got, tt.want)
		}
	}
}
