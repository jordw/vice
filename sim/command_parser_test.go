// sim/command_parser_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/speech"
)

// newCommandTestSim returns a sim with one arrival, AAL123, that is on the
// frequency of the test TCW. It is at 6,000 feet on a route of four fixes
// and expects the ILS runway 22L approach. Position 2B exists and tracks it.
func newCommandTestSim(t *testing.T) (*Sim, av.ADSBCallsign) {
	t.Helper()
	s := NewTestSim(testLogger())
	s.State.Controllers = map[ControlPosition]*av.Controller{"2B": {}}
	s.STARSComputer = makeSTARSComputer("TEST")
	s.LocalCodePool = av.MakeLocalSquawkCodePool(av.LocalSquawkCodePoolSpecifier{})
	s.ERAMComputer = &ERAMComputer{SquawkCodePool: av.MakeEnrouteSquawkCodePool(s.LocalCodePool)}

	ils := &av.Approach{Type: av.ILSApproach, Runway: "22L", FullName: "ILS Runway 22L",
		Waypoints: []av.WaypointArray{{{Fix: "ROSLY"}}}}
	s.State.Airports["KJFK"] = &av.Airport{Approaches: map[string]*av.Approach{"I22L": ils}}

	ac := MakeTestAircraft("AAL123", "22L")
	ac.Nav.Perf = db.DB.AircraftPerformance["A320"]
	ac.Nav.FixAssignments = map[string]nav.FixAssignment{}
	ac.Nav.Approach.Assigned = ils
	for _, fix := range []string{"CAMRN", "ROSLY", "WAVEY", "ZALPO"} {
		p, ok := db.DB.LookupWaypoint(fix)
		if !ok {
			t.Fatalf("%s not found", fix)
		}
		ac.Nav.Waypoints = append(ac.Nav.Waypoints, av.Waypoint{Fix: fix, Location: p})
	}
	camrn := ac.Nav.Waypoints[0].Location
	ac.Nav.FlightState.Position = math.Point2LL{camrn[0], camrn[1] + 10.0/60}
	ac.Nav.FlightState.NmPerLongitude = math.NMPerLongitudeAt(camrn)
	ac.Nav.FlightState.Altitude = 6000
	ac.Nav.FlightState.IAS = 250
	ac.AssociateFlightPlan(&FlightPlan{ACID: ACID(ac.ADSBCallsign), TrackingController: "2B"})
	s.Aircraft[ac.ADSBCallsign] = ac
	return s, ac.ADSBCallsign
}

// commandKind is how RunControlCommands treats a command when it orders the
// commands of a transmission and decides which of them to run.
type commandKind string

const (
	plainCommand       commandKind = "plain command"
	altitudeAssignment commandKind = "altitude assignment"
	approachClearance  commandKind = "approach clearance"
	frequencyChange    commandKind = "frequency change"
	sayAgainRequest    commandKind = "say again request"
)

func (s *Sim) commandKind(command string) commandKind {
	if isAltitudeAssignment(command) {
		return altitudeAssignment
	}
	if _, _, ok := s.parseApproachClearance(command); ok {
		return approachClearance
	}
	if s.changesFrequency(command) {
		return frequencyChange
	}
	if strings.HasPrefix(command, "SAYAGAIN/") {
		return sayAgainRequest
	}
	return plainCommand
}

// TestParseControlCommand checks that each form of the command grammar
// parses, is the kind of command that the transmission logic takes it for,
// and gets the kind of response that its instruction gets. A refusal is the
// right response where the test aircraft cannot follow the instruction.
func TestParseControlCommand(t *testing.T) {
	const (
		altitude  = "speech.AltitudeIntent"
		approach  = "speech.ApproachIntent"
		cleared   = "speech.ClearedApproachIntent"
		contact   = "speech.ContactIntent"
		heading   = "speech.HeadingIntent"
		navigate  = "speech.NavigationIntent"
		none      = "<nil>"
		speed     = "speech.SpeedIntent"
		traffic   = "speech.TrafficAdvisoryIntent"
		transpond = "speech.TransponderIntent"
		unable    = "speech.UnableIntent"
	)
	tests := []struct {
		command string
		kind    commandKind
		intent  string
	}{
		// Headings and turns
		{"H050", plainCommand, heading},
		{"H", plainCommand, heading},
		{"L130", plainCommand, heading},
		{"R210", plainCommand, heading},
		{"T10L", plainCommand, heading},
		{"T20R", plainCommand, heading},
		{"L10D", plainCommand, heading},
		{"R10D", plainCommand, heading},
		{"H120/J", plainCommand, unable},
		{"L120/J", plainCommand, unable},
		{"SH", plainCommand, "speech.ReportHeadingIntent"},

		// Navigation
		{"DWAVEY", plainCommand, navigate},
		{"LDROSLY", plainCommand, navigate},
		{"RDROSLY", plainCommand, navigate},
		{"DROSLY/H180", plainCommand, navigate},
		{"DROSLY/DWAVEY", plainCommand, navigate},
		{"IWAVEY/050", plainCommand, unable},
		{"IWAVEY/050O", plainCommand, unable},
		{"JV1", plainCommand, unable},
		{"RSID", plainCommand, unable},
		{"RSTAR", plainCommand, unable},
		{"RON", plainCommand, unable},
		{"HROSLY", plainCommand, unable},
		{"HROSLY/L/5NM/R090", plainCommand, navigate},
		{"HROSLY/R270/2M", plainCommand, navigate},
		{"EXPDIRROSLY", plainCommand, none},

		// Altitudes
		{"C170", altitudeAssignment, altitude},
		{"D20", altitudeAssignment, altitude},
		{"A120", altitudeAssignment, altitude},
		{"TC170", plainCommand, altitude},
		{"TD20", plainCommand, altitude},
		{"TA120", plainCommand, altitude},
		{"A", plainCommand, unable},
		{"ED", plainCommand, unable},
		{"ED50", plainCommand, unable},
		{"EC", plainCommand, unable},
		{"EC100", plainCommand, unable},
		{"GRD", plainCommand, unable},
		{"GRC", plainCommand, unable},
		{"GR50", plainCommand, unable},
		{"SA", plainCommand, "speech.ReportAltitudeIntent"},

		// Crossing restrictions and procedures
		{"CCAMRN/A110+", plainCommand, navigate},
		{"CROSLY/A30/S210", plainCommand, navigate},
		{"CROSLY/M78", plainCommand, navigate},
		{"CROSLY/5W/A30", plainCommand, unable},
		{"CDME10/A30+", plainCommand, unable},
		{"CSIE/A110", plainCommand, unable},
		{"CVALRE/A50", plainCommand, unable},
		{"CDMESR/A30", plainCommand, unable},
		{"CVS", plainCommand, unable},
		{"CVS/A100", plainCommand, unable},
		{"DVS", plainCommand, unable},
		{"DVS/A120", plainCommand, unable},
		{"AROSLY/C50", plainCommand, altitude},
		{"AROSLY/D30", plainCommand, altitude},
		{"AROSLY/S230", plainCommand, speed},

		// Speeds
		{"S210", plainCommand, speed},
		{"S", plainCommand, speed},
		{"S180+", plainCommand, speed},
		{"S180-", plainCommand, speed},
		{"TS210", plainCommand, speed},
		{"SMIN", plainCommand, speed},
		{"SMAX", plainCommand, speed},
		{"SPRES", plainCommand, speed},
		{"M78", plainCommand, unable},
		{"M78+", plainCommand, unable},
		{"TM78", plainCommand, unable},
		{"S210/U5", plainCommand, speed},
		{"S210/U5DME", plainCommand, speed},
		{"S210/UROSLY", plainCommand, speed},
		{"S250/UROSLY/210", plainCommand, "speech.CompoundSpeedIntent"},
		{"S250/UROSLY/210/UCAMRN/180", plainCommand, "speech.CompoundSpeedIntent"},
		{"SS", plainCommand, "speech.ReportSpeedIntent"},
		{"SI", plainCommand, "speech.ReportSpeedIntent"},
		{"SM", plainCommand, unable},

		// Transponder
		{"SQ1200", plainCommand, transpond},
		{"SQS", plainCommand, transpond},
		{"SQA", plainCommand, transpond},
		{"SQON", plainCommand, transpond},
		{"ID", plainCommand, transpond},

		// Approaches
		{"EI22L", plainCommand, approach},
		{"E", plainCommand, approach},
		{"EVA22L", plainCommand, approach},
		{"CI22L", approachClearance, cleared},
		{"C", approachClearance, cleared},
		{"CSI", approachClearance, cleared},
		{"CSII22L", approachClearance, cleared},
		{"CVA22L", approachClearance, unable},
		{"AROSLY/C", plainCommand, approach},
		{"AROSLY/CI22L", plainCommand, approach},
		{"AROSLY/CSII22L", plainCommand, approach},
		{"AROSLY/I", plainCommand, approach},
		{"I", plainCommand, approach},
		{"CAC", plainCommand, unable},

		// Frequency changes
		{"FC", frequencyChange, contact},
		{"CT2B", frequencyChange, contact},
		{"RST", frequencyChange, contact},
		{"TO", frequencyChange, unable},
		{"TO/119100", frequencyChange, unable},

		// Advisories and the rest
		{"ATIS/B", plainCommand, "speech.ATISIntent"},
		{"AP", plainCommand, "speech.LookForFieldIntent"},
		{"AP/2/8", plainCommand, "speech.LookForFieldIntent"},
		{"RP", plainCommand, unable},
		{"RP/3/3", plainCommand, unable},
		{"RP/BRIDGE", plainCommand, unable},
		{"RP/3/3/BRIDGE", plainCommand, unable},
		{"TRAFFIC", plainCommand, traffic},
		{"TRAFFIC/INSIGHT", plainCommand, traffic},
		{"TRAFFIC/1/5/30", plainCommand, traffic},
		{"TRAFFIC/1/5/UNK", plainCommand, traffic},
		{"VISSEP", plainCommand, unable},
		{"APPROVED", plainCommand, none},
		{"CWT", plainCommand, "speech.CautionWakeTurbulenceIntent"},
		{"GA", plainCommand, none},
		{"AGAIN", plainCommand, none},
		{"SAYAGAIN/HEADING", sayAgainRequest, "speech.SayAgainIntent"},
		{"X", plainCommand, none},
	}
	for _, tc := range tests {
		s, callsign := newCommandTestSim(t)
		if kind := s.commandKind(tc.command); kind != tc.kind {
			t.Errorf("%s: %s, want %s", tc.command, kind, tc.kind)
		}
		intent, err := s.runOneControlCommand(E2ETCW(), callsign, tc.command, 0)
		if err != nil {
			t.Errorf("%s: %v", tc.command, err)
		} else if got := fmt.Sprintf("%T", intent); got != tc.intent {
			t.Errorf("%s: response %s (%+v), want %s", tc.command, got, intent, tc.intent)
		} else if intent != nil {
			speech.RenderIntents([]speech.CommandIntent{intent}, s.textRand)
		}
	}
}

// TestParseControlCommandContact checks that CT is a handoff only if a
// controller has the id that follows it.
func TestParseControlCommandContact(t *testing.T) {
	s, _ := newCommandTestSim(t)
	for command, want := range map[string]commandKind{
		"CT2B": frequencyChange,
		"CTTL": approachClearance,
		"CT":   approachClearance,
	} {
		if kind := s.commandKind(command); kind != want {
			t.Errorf("%s: %s, want %s", command, kind, want)
		}
	}
}

func TestParseControlCommandInvalid(t *testing.T) {
	s, callsign := newCommandTestSim(t)
	for _, command := range []string{
		"", "Q", "5", "/", "F", "FCX", "G", "GRX", "V", "VISSEPX", "NOTCLEARED",
		"D", "DAB", "DABCDEF", "DROSLY/X180", "I/", "IWAVEY", "IWAVEY/361", "J", "HROSLY/INVALID/R090",
		"AROSLY/", "AROSLY/X", "AROSLY/C/I", "AROSLY/D", "AP/13/8", "AP/2",
		"CDME10", "CDME10/X", "CROSLY/X", "CROSLY/A", "CROSLY/A50-30", "CVS/100", "DVS/A601",
		"M7", "M788", "T", "TL", "T5X", "TX50", "TAX", "LABC", "DROSLY/HX", "S2X0", "AROSLY/SX",
		"C99999999999999999999",
		"TRAFFIC/1/5", "TRAFFIC/13/5/30", "TRAFFIC/1/0/30", "TRAFFIC/1/5/X",
		"RP/", "RP/3/3/", "RP/1/2/3/4", "SAYAGAIN/BOGUS",
		"H-5", "H+20", "L-5", "R-5D", "T-5L", "TC-50", "DROSLY/H-180", "CCAMRN/A-10+",
		"CDME10/A-30+", "S180--5", "M-7", "TRAFFIC/1/5/-30",
	} {
		if _, err := s.runOneControlCommand(E2ETCW(), callsign, command, 0); err != ErrInvalidCommandSyntax {
			t.Errorf("%q: got error %v, want %v", command, err, ErrInvalidCommandSyntax)
		}
	}
}

// TestParseSTTTestCommands checks that the sim can parse every command that
// the speech recognition tests expect the decoder to give it.
func TestParseSTTTestCommands(t *testing.T) {
	for _, dir := range []string{"../speech/stt/tests", "../speech/stt/failing_tests"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no test files: %v", dir, err)
		}
		for _, file := range files {
			contents, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var test struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(contents, &test); err != nil {
				t.Fatalf("%s: %v", file, err)
			}

			commands := strings.Fields(test.Command)
			if len(commands) > 0 && (commands[0] == correctionCommand || commands[0] == rollbackCommand) {
				commands = commands[1:]
			}
			for _, command := range commands {
				s, callsign := newCommandTestSim(t)
				res := s.RunControlCommands(E2ETCW(), callsign, command, 0, 0, "", false)
				if res.Error != nil {
					t.Errorf("%s: %s: %v", filepath.Base(file), command, res.Error)
				}
			}
		}
	}
}
