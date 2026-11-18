// sim/command_parser.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Aircraft command dispatch

var ErrInvalidCommandSyntax = fmt.Errorf("invalid command syntax")

type ControlCommandsResult struct {
	RemainingInput     string
	Error              error
	ReadbackSpokenText string          // Spoken text for TTS synthesis (if readback was generated)
	ReadbackCallsign   av.ADSBCallsign // Aircraft callsign for the readback
}

// RunAircraftControlCommands executes a space-separated string of control commands for an aircraft
// on behalf of the controller at tcw, recording the correction history that lets a follow-up
// "correction" revise or undo the transmission.
// Returns the remaining unparsed input and any error that occurred.
func (s *Sim) RunAircraftControlCommands(tcw TCW, callsign av.ADSBCallsign, commandStr string,
	audioDuration, sttDuration time.Duration, transcript string) ControlCommandsResult {
	res := s.RunControlCommands(tcw, callsign, commandStr, audioDuration, sttDuration, transcript, true)
	s.publish()
	return res
}

// RunScriptedControlCommands executes scenario-scripted commands. They are not controller
// transmissions, so they leave the correction history of whoever is at tcw alone.
func (s *Sim) RunScriptedControlCommands(tcw TCW, callsign av.ADSBCallsign, commandStr string) ControlCommandsResult {
	res := s.RunControlCommands(tcw, callsign, commandStr, 0, 0, "", false)
	s.publish()
	return res
}

// RunControlCommands is the core command execution logic shared by the dispatcher and automated
// test code. All intents from commands are collected and rendered together as a single
// transmission. For voice commands, audioDuration is the length of the transmission,
// sttDuration is the time from PTT release until it was decoded, and transcript is what the
// speech recognizer heard; they are zero and empty for typed or non-voice commands. Pilot
// reaction delays are measured from when the pilot heard each instruction: the callsign and
// each command are taken to use an equal share of the audio, so the time from hearing a
// command until it is executed is sttDuration plus a share for each command that follows it.
// It does not publish; callers should generally go through RunAircraftControlCommands or
// RunScriptedControlCommands, which do.
func (s *Sim) RunControlCommands(tcw TCW, callsign av.ADSBCallsign, commandStr string,
	audioDuration, sttDuration time.Duration, transcript string, recordHistory bool) ControlCommandsResult {
	commands := strings.Fields(commandStr)

	// A leading correction marker was spoken too, so it takes a share.
	audioShare := audioDuration / time.Duration(1+len(commands))

	// Parse addressing form suffix from callsign: /T indicates type+trailing3 addressing
	// (e.g., "skyhawk 3 alpha bravo" instead of "november 1 2 3 alpha bravo")
	addressingForm := AddressingFormFull
	if cs, ok := strings.CutSuffix(string(callsign), "/T"); ok {
		addressingForm = AddressingFormTypeTrailing3
		callsign = av.ADSBCallsign(cs)
	}

	marker := ""
	if len(commands) > 0 && (commands[0] == correctionCommand || commands[0] == rollbackCommand) {
		marker, commands = commands[0], commands[1:]
	}

	// Update aircraft's last addressing form for readback rendering
	if ac, ok := s.Aircraft[callsign]; ok {
		ac.LastAddressingForm = addressingForm
	}
	// "Correction" retracts the previous transmission only when it re-addresses a
	// different aircraft. Naming the same one amends what the controller said to it,
	// leaving the instructions the pilot got right in place.
	last := s.lastSTTCommands[tcw]
	if marker == rollbackCommand || (marker == correctionCommand && last != nil && last.Callsign != callsign) {
		// A failed undo must not swallow the instruction that follows it.
		if err := s.rollbackLastCommand(tcw); err != nil {
			s.lg.Warnf("%s: could not undo the previous transmission: %v", callsign, err)
		}
	}

	if marker != "" && len(commands) == 0 {
		return ControlCommandsResult{}
	}

	// Handle special STT commands that need direct TTS synthesis
	// These short-circuit normal command processing
	if len(commands) == 1 {
		switch commands[0] {
		case "AGAIN":
			cs, spokenText, err := s.SayAgain(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		case "NOTCLEARED":
			cs, spokenText, err := s.SayNotCleared(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		}
	}

	// Take a snapshot before executing commands, so that a following "correction"
	// can undo them.
	if recordHistory {
		if ac, ok := s.Aircraft[callsign]; ok && s.TCWCanCommandAircraft(tcw, ac) {
			s.lastSTTCommands[tcw] = &lastSTTCommand{
				Callsign:      callsign,
				NavSnapshot:   ac.Nav.TakeSnapshot(),
				ReportedATIS:  ac.ReportedATIS,
				Transmissions: len(ac.ControlTransmissions),
			}
		}
	}

	// The readback and the record of the transmission keep the spoken
	// order, whatever order the commands run in.
	intents := make([]speech.CommandIntent, len(commands))
	ran := make([]bool, len(commands))
	var cmdErr error
	var remaining string
	refused := false

	order, clearance := s.executionOrder(commands)
	for k, i := range order {
		command := commands[i]
		// A pilot who refused part of the transmission stays on frequency so
		// that the controller can amend it.
		if refused && s.changesFrequency(command) {
			continue
		}

		run := s.runOneControlCommand
		if clearance && isAltitudeAssignment(command) {
			run = s.runAltitudeBeforeClearance
		}
		delayReduction := sttDuration + time.Duration(len(commands)-1-i)*audioShare
		intent, err := run(tcw, callsign, command, delayReduction)
		if err != nil {
			cmdErr = err
			remaining = strings.Join(util.MapSlice(slices.Sorted(slices.Values(order[k:])),
				func(j int) string { return commands[j] }), " ")
			break
		}
		ran[i] = true
		intents[i] = intent

		// The controller repeats the transmission after a pilot asks for part
		// of it again, so the rest of it is moot.
		if strings.HasPrefix(command, "SAYAGAIN/") {
			break
		}
		if _, ok := intent.(speech.Refusal); ok {
			refused = true
		}
	}
	intents = slices.DeleteFunc(intents, func(intent speech.CommandIntent) bool { return intent == nil })
	var executed []string
	for i, command := range commands {
		if ran[i] {
			executed = append(executed, command)
		}
	}

	// The aircraft is gone if one of the commands deleted it.
	if ac, ok := s.Aircraft[callsign]; ok && len(executed) > 0 {
		ac.ControlTransmissions = append(ac.ControlTransmissions, ControlTransmission{
			Commands:   strings.Join(executed, " "),
			Transcript: transcript,
		})
	}

	// Render all intents together as a single transmission, including the ones collected
	// before a command failed.
	spokenText := s.renderAndPostReadback(callsign, tcw, intents)

	return ControlCommandsResult{
		RemainingInput:     remaining,
		Error:              cmdErr,
		ReadbackSpokenText: spokenText,
		ReadbackCallsign:   callsign,
	}
}

// executionOrder returns the indices of a transmission's commands in the
// order to run them: as spoken, except that an altitude assignment after an
// approach clearance runs just before the clearance. A pilot takes an
// altitude given in the same transmission as the clearance as one given
// before it. It also reports whether the transmission has an approach
// clearance.
func (s *Sim) executionOrder(commands []string) (order []int, clearance bool) {
	at := -1 // where the approach clearance is in order
	for i, command := range commands {
		if at >= 0 && isAltitudeAssignment(command) {
			order = slices.Insert(order, at, i)
			at++
			continue
		}
		if _, _, ok := s.parseApproachClearance(command); ok && at < 0 {
			at = len(order)
		}
		order = append(order, i)
	}
	return order, at >= 0
}

// runAltitudeBeforeClearance runs an altitude assignment given in the same
// transmission as an approach clearance.
func (s *Sim) runAltitudeBeforeClearance(tcw TCW, callsign av.ADSBCallsign, command string,
	delayReduction time.Duration) (speech.CommandIntent, error) {
	alt, err := assignedAltitude(command)
	if err != nil {
		return nil, err
	}
	return s.AssignAltitudeBeforeClearance(tcw, callsign, alt, delayReduction)
}

// Markers that the STT decoder puts ahead of the commands when the controller
// revised what they had just transmitted.
const (
	// correctionCommand marks a transmission that opened with "correction".
	correctionCommand = "CORRECTION"
	// rollbackCommand marks one that retracts the previous transmission outright,
	// e.g. "that was not for you".
	rollbackCommand = "ROLLBACK"
)

// rollbackLastCommand restores the state of the aircraft that the controller at tcw last
// transmitted to, undoing that transmission.
func (s *Sim) rollbackLastCommand(tcw TCW) error {
	last := s.lastSTTCommands[tcw]
	if last == nil {
		return ErrNoRecentCommand
	}
	// Consecutive rollbacks should fail: there is only ever one transmission to undo.
	delete(s.lastSTTCommands, tcw)

	ac, ok := s.Aircraft[last.Callsign]
	if !ok {
		return ErrNoRecentCommand
	}
	if !s.TCWCanCommandAircraft(tcw, ac) {
		return av.ErrOtherControllerHasTrack
	}

	ac.Nav.RestoreSnapshot(last.NavSnapshot)
	ac.ReportedATIS = last.ReportedATIS
	for i := last.Transmissions; i < len(ac.ControlTransmissions); i++ {
		ac.ControlTransmissions[i].RolledBack = true
	}
	return nil
}

// lastAddressedCallsign returns the aircraft that the controller at tcw last transmitted
// to, or "" if there is none they can still command. Clients need it to attribute a
// correction that names no aircraft. It carries the /T suffix when the controller
// addressed the aircraft by type and trailing three, so that the correction goes back
// to them in the form they have been using.
func (s *Sim) lastAddressedCallsign(tcw TCW) av.ADSBCallsign {
	last, ok := s.lastSTTCommands[tcw]
	if !ok {
		return ""
	}
	ac, ok := s.Aircraft[last.Callsign]
	if !ok || !s.TCWCanCommandAircraft(tcw, ac) {
		return ""
	}
	if ac.LastAddressingForm == AddressingFormTypeTrailing3 {
		return last.Callsign + "/T"
	}
	return last.Callsign
}

// ClearSTTCommands discards the correction history for a TCW that has become unoccupied.
func (s *Sim) ClearSTTCommands(tcw TCW) {
	delete(s.lastSTTCommands, tcw)
}

// clearAircraftSTTCommands discards any correction history naming the given aircraft; it is
// no longer a valid target once the aircraft leaves the controller's frequency or the sim.
func (s *Sim) clearAircraftSTTCommands(callsign av.ADSBCallsign) {
	for tcw, last := range s.lastSTTCommands {
		if last.Callsign == callsign {
			delete(s.lastSTTCommands, tcw)
		}
	}
}

// changesFrequency reports whether command sends the aircraft off the
// controller's frequency.
func (s *Sim) changesFrequency(command string) bool {
	if command == "TO" || strings.HasPrefix(command, "TO/") || command == "FC" || command == "RST" {
		return true
	}
	if tcp, ok := strings.CutPrefix(command, "CT"); ok {
		_, ok := s.State.Controllers[TCP(tcp)]
		return ok
	}
	return false
}

// renderAndPostReadback renders a batch of command intents as a pilot readback transmission.
// The tcw ensures the readback goes to the controller who issued the command,
// regardless of any consolidation changes.
// Returns the spoken text for TTS synthesis, including the callsign suffix.
func (s *Sim) renderAndPostReadback(callsign av.ADSBCallsign, tcw TCW, intents []speech.CommandIntent) string {
	if rt := speech.RenderIntents(intents, s.textRand); rt != nil {
		return s.postReadbackTransmission(callsign, rt, tcw)
	}
	return ""
}

// parseNumber parses a number in a command. A number is always a string of
// digits: a sign is never part of one.
func parseNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || !util.IsAllNumbers(s) {
		return 0, ErrInvalidCommandSyntax
	}
	return n, nil
}

// parseSpeedRestriction parses the speed restriction of a command.
func parseSpeedRestriction(s string) (*av.SpeedRestriction, error) {
	sr, err := av.ParseSpeedRestriction(s)
	if err != nil || sr.Range[0] < 0 || sr.Range[1] < 0 {
		return nil, ErrInvalidCommandSyntax
	}
	return sr, nil
}

// parseAltitudeRestriction parses the altitude restriction of a command,
// which gives altitudes in hundreds of feet, and returns it in feet.
func parseAltitudeRestriction(s string) (*av.AltitudeRestriction, error) {
	ar, err := av.ParseAltitudeRestriction(s)
	if err != nil || ar.Range[0] < 0 || ar.Range[1] < 0 {
		return nil, ErrInvalidCommandSyntax
	}
	ar.Range[0] *= 100
	if ar.Range[1] != av.MaxAltitude {
		ar.Range[1] *= 100
	}
	return ar, nil
}

// parseSpeedUntil parses the "until" specification from a speed command.
// Formats:
//   - "ROSLY" -> fix name
//   - "5DME"  -> 5 DME
//   - "6"     -> 6 mile final
func parseSpeedUntil(untilStr string) *speech.SpeedUntil {
	untilStr = strings.ToUpper(untilStr)

	// Check for DME pattern: digits followed by DME
	if before, ok := strings.CutSuffix(untilStr, "DME"); ok {
		if n, err := parseNumber(before); err == nil && n > 0 {
			return &speech.SpeedUntil{DME: n}
		}
	}

	// Check for pure number (mile final)
	if n, err := parseNumber(untilStr); err == nil && n > 0 {
		return &speech.SpeedUntil{MileFinal: n}
	}

	// Otherwise it's a fix name
	return &speech.SpeedUntil{Fix: untilStr}
}

// parseCompoundSpeed parses a compound speed command string like
// "250+/UFIX1/210-/UFIX2/180+" into CompoundSpeedSegments.
// The input is the part after 'S' (e.g., "250+/UFIX1/210-/UFIX2/180+").
func parseCompoundSpeed(s string) ([]speech.CompoundSpeedSegment, error) {
	// Split on "/U" to get alternating speed/fix pairs.
	// First element is the first speed, then alternating fix+speed pairs.
	parts := strings.Split(s, "/U")
	if len(parts) < 2 {
		return nil, ErrInvalidCommandSyntax
	}

	var segments []speech.CompoundSpeedSegment

	// First part is just the first speed.
	sr, err := parseSpeedRestriction(parts[0])
	if err != nil {
		return nil, err
	}

	// For parts[1..n-1], each contains "FIX/SPEED" (the fix for the previous
	// segment's UntilFix, followed by the next speed after a "/").
	// The last part may be just "FIX/SPEED" or just "FIX" (if the last
	// segment has no further speed — but per the plan, the last segment is
	// always an open-ended speed).
	for i := 1; i < len(parts); i++ {
		fix, speedStr, hasSpeed := strings.Cut(parts[i], "/")
		if fix == "" {
			return nil, ErrInvalidCommandSyntax
		}

		// Close out the previous segment with this fix.
		segments = append(segments, speech.CompoundSpeedSegment{
			Speed:    sr,
			UntilFix: fix,
		})

		if hasSpeed {
			// Parse the next speed.
			sr, err = parseSpeedRestriction(speedStr)
			if err != nil {
				return nil, err
			}
		} else if i < len(parts)-1 {
			// Not the last part but no speed — invalid.
			return nil, ErrInvalidCommandSyntax
		} else {
			// Last part with no trailing speed — no final open-ended segment.
			return segments, nil
		}
	}

	// Add the final open-ended segment (no UntilFix).
	segments = append(segments, speech.CompoundSpeedSegment{
		Speed: sr,
	})

	return segments, nil
}

// parseHeading parses the heading of an H, L, or R command, which may be
// given "to join" with a /J suffix: "120" or "120/J".
func parseHeading(spec string) (hdg int, toJoin bool, err error) {
	spec, toJoin = strings.CutSuffix(spec, "/J")
	hdg, err = parseNumber(spec)
	return
}

// parseInterceptRadial parses the argument of an intercept-radial command,
// "FIX/radial" or "FIX/radialO", and returns the fix, the radial, and whether
// it is to be flown outbound. Inbound is the default.
func parseInterceptRadial(command string) (string, int, bool, bool) {
	fix, spec, ok := strings.Cut(strings.ToUpper(command), "/")
	if !ok || fix == "" {
		return "", 0, false, false
	}

	outbound := false
	if rest, ok := strings.CutSuffix(spec, "O"); ok {
		spec, outbound = rest, true
	} else if rest, ok := strings.CutSuffix(spec, "I"); ok {
		spec = rest
	}

	radial, err := parseNumber(spec)
	if err != nil || radial < 1 || radial > 360 {
		return "", 0, false, false
	}

	return fix, radial, outbound, true
}

// parseHold parses a hold command string in the format "FIX/[option]/[option]"
// and returns the fix name and a controller-specified hold if options are present.
// Returns (fixName, nil, true) if no options are specified (use published hold).
// Returns (fixName, *Hold, true) if options are successfully parsed.
// Returns ("", nil, false) if parsing fails.
//
// Options may be:
// - L: left turns
// - R: right turns
// - xxNM: xx nautical mile legs
// - xxM: xx minute legs
// - Rxxx: inbound course on the xxx radial to the fix
//
// If options are specified, the Rxxx radial option is required.
// Multiple options of the same type result in an error.
func parseHold(command string) (string, *av.Hold, bool) {
	fix, opts, ok := strings.Cut(command, "/")
	fix = strings.ToUpper(fix)
	if strings.ContainsAny(fix, "+-") {
		// No fix name has a sign, so this is a malformed heading.
		return "", nil, false
	}
	if !ok {
		// No options, use published hold
		return fix, nil, true
	}

	// Controller-specified hold with defaults
	hold := av.Hold{Fix: fix}
	directionSet := false

	for opt := range strings.SplitSeq(opts, "/") {
		opt = strings.ToUpper(opt)

		switch {
		case opt == "L":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnLeft
			directionSet = true

		case opt == "R":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnRight
			directionSet = true

		case strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			dist, err := strconv.ParseFloat(strings.TrimSuffix(opt, "NM"), 32)
			if err != nil || dist <= 0 {
				return "", nil, false
			}
			hold.LegLengthNM = float32(dist)

		case strings.HasSuffix(opt, "M") && !strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			time, err := strconv.ParseFloat(strings.TrimSuffix(opt, "M"), 32)
			if err != nil || time <= 0 {
				return "", nil, false
			}
			hold.LegMinutes = float32(time)

		case strings.HasPrefix(opt, "R") && len(opt) > 1:
			if hold.InboundCourse != 0 {
				return "", nil, false
			}
			radial, err := parseNumber(opt[1:])
			if err != nil || radial <= 0 || radial > 360 {
				return "", nil, false
			}
			hold.InboundCourse = math.MagneticHeading(radial)

		default:
			return "", nil, false
		}
	}

	// Radial is required for controller-specified holds
	if hold.InboundCourse == 0 {
		return "", nil, false
	}
	if !directionSet {
		hold.TurnDirection = av.TurnRight
	}
	if hold.LegMinutes == 0 && hold.LegLengthNM == 0 {
		hold.LegMinutes = 1
	}

	return fix, &hold, true
}

// parseViaExceptAltitude parses the "A<alt>" argument of CVS and DVS: the
// "except maintain" altitude, in hundreds of feet.
func parseViaExceptAltitude(arg string) (*float32, error) {
	digits, ok := strings.CutPrefix(arg, "A")
	if !ok {
		return nil, ErrInvalidCommandSyntax
	}
	alt, err := parseNumber(digits)
	if err != nil || alt < 1 || alt > 600 {
		return nil, ErrInvalidCommandSyntax
	}
	feet := float32(100 * alt)
	return &feet, nil
}

// parseOclockMiles parses the "oclock/miles" arguments of a position call
// like AP/3/5.
func parseOclockMiles(s string) (oclock, miles int, err error) {
	clockstr, milesstr, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, ErrInvalidCommandSyntax
	}
	oclock, err = parseNumber(clockstr)
	if err != nil || oclock < 1 || oclock > 12 {
		return 0, 0, ErrInvalidCommandSyntax
	}
	miles, err = parseNumber(milesstr)
	if err != nil || miles < 1 || miles > 50 {
		return 0, 0, ErrInvalidCommandSyntax
	}
	return oclock, miles, nil
}

// isAltitudeAssignment reports whether command is A###, C###, or D###, which
// all assign an altitude.
func isAltitudeAssignment(command string) bool {
	return len(command) > 1 && strings.ContainsRune("ACD", rune(command[0])) && util.IsAllNumbers(command[1:])
}

// assignedAltitude returns the altitude in feet that an altitude assignment
// (isAltitudeAssignment) gives.
func assignedAltitude(command string) (int, error) {
	alt, err := parseNumber(command[1:])
	if err != nil {
		return 0, err
	}
	if alt > 600 && (alt%100 == 0) {
		// Sometimes STT transcript interpretation forgets altitudes are in 100s...
		alt /= 100
	}
	return 100 * alt, nil
}

// parseApproachClearance parses a command that clears the aircraft for an
// approach: CVA and a runway for a visual; CSI and the approach, or the
// expected one, for a straight-in; or C and the approach, the expected one
// if it is left out. It returns the approach as ClearedApproach takes it.
func (s *Sim) parseApproachClearance(command string) (approach string, straightIn bool, ok bool) {
	rest, isC := strings.CutPrefix(command, "C")
	if !isC || isAltitudeAssignment(command) {
		return "", false, false
	}
	// The other commands that start with C. An approach id never has a "/",
	// so a crossing restriction at a fix whose name starts with SI, VA, or
	// DME is not a clearance.
	if rest == "AC" || rest == "WT" || rest == "VS" || strings.Contains(rest, "/") {
		return "", false, false
	}
	if id, ok := strings.CutPrefix(rest, "VA"); ok && id != "" {
		return "_VIS" + id, true, true
	}
	if spec, ok := strings.CutPrefix(rest, "DME"); ok && spec != "" {
		return "", false, false
	}
	if appr, ok := strings.CutPrefix(rest, "SI"); ok && (appr == "" || !util.IsAllNumbers(appr)) {
		return appr, true, true
	}
	// CT is a contact command for an existing controller, otherwise the
	// clearance for an approach whose id starts with T.
	if tcp, ok := strings.CutPrefix(rest, "T"); ok && tcp != "" {
		if _, ok := s.State.Controllers[TCP(tcp)]; ok {
			return "", false, false
		}
	}
	return rest, false, true
}

// runOneControlCommand executes a single control command for an aircraft.
// Returns the intent generated by the command (if any) for batching.
// delayReduction, the time since the pilot heard the command, is subtracted
// from the pilot's delay in acting on it, floored at zero.
func (s *Sim) runOneControlCommand(tcw TCW, callsign av.ADSBCallsign, command string, delayReduction time.Duration) (speech.CommandIntent, error) {
	if len(command) == 0 {
		return nil, ErrInvalidCommandSyntax
	}

	if isAltitudeAssignment(command) {
		alt, err := assignedAltitude(command)
		if err != nil {
			return nil, err
		}
		return s.AssignAltitude(tcw, callsign, alt, false, delayReduction)
	}

	switch command[0] {
	case 'A':
		if command == "A" {
			return s.AltitudeOurDiscretion(tcw, callsign)
		} else if command == "APPROVED" {
			return s.ApproveVisualSeparation(tcw, callsign)
		} else if command == "AGAIN" {
			// AGAIN is handled specially in RunControlCommands for TTS synthesis
			return nil, nil
		} else if command == "AP" {
			return s.AirportInSightInquiry(tcw, callsign)
		} else if remainder, ok := strings.CutPrefix(command, "AP/"); ok {
			oclock, miles, err := parseOclockMiles(remainder)
			if err != nil {
				return nil, err
			}
			return s.AirportAdvisory(tcw, callsign, oclock, miles)
		} else if letter, ok := strings.CutPrefix(command, "ATIS/"); ok {
			return s.ATISCommand(tcw, callsign, letter)
		} else {
			components := strings.Split(command, "/")
			if len(components) != 2 || len(components[1]) == 0 {
				return nil, ErrInvalidCommandSyntax
			}

			fix := strings.ToUpper(components[0][1:])
			switch components[1][0] {
			case 'C':
				rest, straightIn := strings.CutPrefix(components[1][1:], "SI")
				if alt, err := parseNumber(rest); err == nil {
					return s.AfterFixAltitude(tcw, callsign, fix, alt*100)
				}
				return s.AtFixCleared(tcw, callsign, fix, rest, straightIn)
			case 'D':
				alt, err := parseNumber(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.AfterFixAltitude(tcw, callsign, fix, alt*100)
			case 'I':
				return s.AtFixIntercept(tcw, callsign, fix)
			case 'S':
				sr, err := parseSpeedRestriction(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.AfterFixSpeed(tcw, callsign, fix, sr)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		}

	case 'C':
		if approach, straightIn, ok := s.parseApproachClearance(command); ok {
			return s.ClearedApproach(tcw, callsign, approach, straightIn, delayReduction)
		}
		if command == "CAC" {
			return s.CancelApproachClearance(tcw, callsign)
		} else if command == "CWT" {
			return s.CautionWakeTurbulence(tcw, callsign)
		} else if command == "CVS" {
			return s.ClimbViaSID(tcw, callsign, nil, delayReduction)
		} else if arg, ok := strings.CutPrefix(command, "CVS/"); ok {
			alt, err := parseViaExceptAltitude(arg)
			if err != nil {
				return nil, err
			}
			return s.ClimbViaSID(tcw, callsign, alt, delayReduction)
		} else if components := strings.Split(command, "/"); len(components) > 1 {
			var ar *av.AltitudeRestriction
			var sr *av.SpeedRestriction
			dist := float32(-1)
			var dir math.CardinalOrdinalDirection
			for _, cmd := range components[1:] {
				var err error
				switch {
				case cmd == "":
					err = ErrInvalidCommandSyntax
				case cmd[0] == 'A' && len(cmd) > 1:
					ar, err = parseAltitudeRestriction(cmd[1:])
				case cmd[0] == 'S':
					sr, err = parseSpeedRestriction(cmd[1:])
				case cmd[0] == 'M':
					sr, err = parseSpeedRestriction(cmd)
				default:
					var d int
					d, dir, err = av.ParseDistanceDirection(cmd)
					dist = float32(d)
				}
				if err != nil {
					return nil, ErrInvalidCommandSyntax
				}
			}

			// DME and a number is a distance on the DME. Anything else is a
			// fix, even one whose name starts with DME.
			fix := components[0][1:]
			if dme, ok := strings.CutPrefix(fix, "DME"); ok {
				if d, err := parseNumber(dme); err == nil {
					if dist >= 0 || (ar == nil && sr == nil) {
						return nil, ErrInvalidCommandSyntax
					}
					return s.CrossDMEAt(tcw, callsign, float32(d), ar, sr)
				}
			}
			if dist >= 0 {
				return s.CrossDistanceFromFixAt(tcw, callsign, fix, dist, dir, ar, sr)
			}
			return s.CrossFixAt(tcw, callsign, fix, ar, sr)
		} else if tcp, ok := strings.CutPrefix(command, "CT"); ok {
			// parseApproachClearance takes CT followed by anything but a
			// controller as an approach clearance.
			return s.ContactController(tcw, callsign, TCP(tcp), delayReduction)
		}
		return nil, ErrInvalidCommandSyntax

	case 'D':
		if command == "DVS" {
			return s.DescendViaSTAR(tcw, callsign, nil, delayReduction)
		} else if arg, ok := strings.CutPrefix(command, "DVS/"); ok {
			alt, err := parseViaExceptAltitude(arg)
			if err != nil {
				return nil, err
			}
			return s.DescendViaSTAR(tcw, callsign, alt, delayReduction)
		} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
			fix := components[0][1:]

			switch components[1][0] {
			case 'D':
				return s.DepartFixDirect(tcw, callsign, fix, components[1][1:])
			case 'H':
				hdg, err := parseNumber(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.DepartFixHeading(tcw, callsign, fix, hdg)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else if len(command) >= 4 && len(command) <= 6 {
			return s.DirectFix(tcw, callsign, command[1:], av.TurnClosest, delayReduction)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'E':
		if len(command) > 1 && s.hasApproach(callsign, command[1:]) {
			// An approach named like an expedite command, such as KLNS's
			// D26, is still an approach to expect.
			return s.ExpectApproach(tcw, callsign, command[1:])
		}
		if command == "ED" {
			return s.ExpediteDescent(tcw, callsign)
		} else if a, ok := strings.CutPrefix(command, "ED"); ok && len(a) > 0 {
			alt, err := parseNumber(a)
			if err == nil {
				return s.ExpediteDescentThrough(tcw, callsign, float32(alt*100))
			}
			// Fall through to expect-approach parsing below
		}
		if command == "EC" {
			return s.ExpediteClimb(tcw, callsign)
		} else if a, ok := strings.CutPrefix(command, "EC"); ok && len(a) > 0 {
			alt, err := parseNumber(a)
			if err == nil {
				return s.ExpediteClimbThrough(tcw, callsign, float32(alt*100))
			}
			// Fall through
		}
		if id, ok := strings.CutPrefix(command, "EVA"); ok && len(id) > 0 { // Expect visual
			return s.ExpectApproach(tcw, callsign, "_VIS"+id)
		} else if fix, ok := strings.CutPrefix(command, "EXPDIR"); ok && len(fix) > 0 {
			return s.ExpectDirect(tcw, callsign, fix)
		} else if len(command) > 1 {
			return s.ExpectApproach(tcw, callsign, command[1:])
		} else if command == "E" {
			// Bare "E" re-issues expect for the already-assigned approach
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.AssignedId != "" {
				return s.ExpectApproach(tcw, callsign, ac.Nav.Approach.AssignedId)
			}
			return speech.MakeUnableIntent("unable. We haven't been told to expect an approach"), nil
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'F':
		if command == "FC" {
			// An aircraft cleared for the approach whose track hasn't been
			// handed off is going to the tower; STT also sometimes gives FC
			// for a garbled "contact tower".
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.Cleared && !s.trackHandedOff(ac) {
				return s.ContactTower(tcw, callsign, av.Frequency(0))
			}
			return s.ContactTrackingController(tcw, ACID(callsign), delayReduction)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'G':
		if command == "GA" {
			if err := s.GoAhead(tcw, callsign); err != nil {
				return nil, err
			}
			return nil, nil // GoAhead returns no intent
		} else if command == "GRD" {
			return s.GoodRateDescent(tcw, callsign)
		} else if command == "GRC" {
			return s.GoodRateClimb(tcw, callsign)
		} else if a, ok := strings.CutPrefix(command, "GR"); ok && len(a) > 0 {
			alt, err := parseNumber(a)
			if err != nil {
				return nil, err
			}
			return s.GoodRateThrough(tcw, callsign, float32(alt*100))
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'H':
		if len(command) == 1 {
			// Present heading
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Present:        true,
				DelayReduction: delayReduction,
			})
		} else if hdg, toJoin, err := parseHeading(command[1:]); err == nil {
			// Fly heading xxx
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnClosest,
				ToJoin:         toJoin,
				DelayReduction: delayReduction,
			})
		} else {
			// Hold at fix (published or controller-specified)
			if fix, hold, ok := parseHold(command[1:]); !ok {
				return nil, ErrInvalidCommandSyntax
			} else {
				return s.HoldAtFix(tcw, callsign, fix, hold)
			}
		}

	case 'I':
		if len(command) == 1 {
			return s.InterceptApproach(tcw, callsign)
		} else if command == "ID" {
			return s.Ident(tcw, callsign, delayReduction)
		} else if fix, radial, outbound, ok := parseInterceptRadial(command[1:]); ok {
			return s.InterceptRadial(tcw, callsign, fix, radial, outbound, delayReduction)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'J':
		if len(command) == 1 {
			return nil, ErrInvalidCommandSyntax
		}
		return s.JoinAirway(tcw, callsign, command[1:], delayReduction)

	case 'L':
		if len(command) >= 5 && command[1] == 'D' {
			return s.DirectFix(tcw, callsign, command[2:], av.TurnLeft, delayReduction)
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := parseNumber(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				LeftDegrees:    deg,
				DelayReduction: delayReduction,
			})
		} else {
			hdg, toJoin, err := parseHeading(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnLeft,
				ToJoin:         toJoin,
				DelayReduction: delayReduction,
			})
		}
	case 'M': // Mach number: M78, M78+ (or greater), M78- (or less)
		sr, err := parseSpeedRestriction(command)
		if err != nil || len(strings.TrimRight(command, "+-")) != 3 {
			return nil, ErrInvalidCommandSyntax
		}
		return s.AssignSpeed(tcw, callsign, sr, false)

	case 'R':
		if command == "RON" {
			return s.ResumeOwnNavigation(tcw, callsign)
		} else if command == "RSID" {
			return s.ResumeSID(tcw, callsign, delayReduction)
		} else if command == "RSTAR" {
			return s.ResumeSTAR(tcw, callsign, delayReduction)
		} else if command == "RST" {
			return s.RadarServicesTerminated(tcw, callsign, delayReduction)
		} else if command == "RP" {
			return s.ReportingPointAdvisory(tcw, callsign, "", 0, 0)
		} else if remainder, ok := strings.CutPrefix(command, "RP/"); ok {
			// RP/id, RP/oclock/miles, or RP/oclock/miles/id.
			parts := strings.Split(remainder, "/")
			if len(parts) > 3 {
				return nil, ErrInvalidCommandSyntax
			}
			var id string
			var oclock, miles int
			if len(parts) != 2 {
				if id = parts[len(parts)-1]; id == "" {
					return nil, ErrInvalidCommandSyntax
				}
			}
			if len(parts) > 1 {
				var err error
				oclock, miles, err = parseOclockMiles(parts[0] + "/" + parts[1])
				if err != nil {
					return nil, err
				}
			}
			return s.ReportingPointAdvisory(tcw, callsign, id, oclock, miles)
		} else if len(command) >= 5 && command[1] == 'D' {
			return s.DirectFix(tcw, callsign, command[2:], av.TurnRight, delayReduction)
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := parseNumber(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				RightDegrees:   deg,
				DelayReduction: delayReduction,
			})
		} else {
			hdg, toJoin, err := parseHeading(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnRight,
				ToJoin:         toJoin,
				DelayReduction: delayReduction,
			})
		}

	case 'S':
		if len(command) == 1 {
			return s.AssignSpeed(tcw, callsign, nil, false)
		} else if command == "SPRES" {
			return s.MaintainPresentSpeed(tcw, callsign)
		} else if command == "SMIN" {
			return s.MaintainSlowestPractical(tcw, callsign)
		} else if command == "SMAX" {
			return s.MaintainMaximumForward(tcw, callsign)
		} else if command == "SS" {
			return s.SaySpeed(tcw, callsign)
		} else if command == "SI" {
			return s.SayIndicatedSpeed(tcw, callsign)
		} else if command == "SM" {
			return s.SayMach(tcw, callsign)
		} else if command == "SQS" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeStandby, delayReduction)
		} else if command == "SQA" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeAltitude, delayReduction)
		} else if command == "SQON" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeOn, delayReduction)
		} else if len(command) == 6 && command[:2] == "SQ" {
			sq, err := av.ParseSquawk(command[2:])
			if err != nil {
				return nil, err
			}
			return s.ChangeSquawk(tcw, callsign, sq, delayReduction)
		} else if command == "SH" {
			return s.SayHeading(tcw, callsign)
		} else if command == "SA" {
			return s.SayAltitude(tcw, callsign)
		} else if what, ok := strings.CutPrefix(command, "SAYAGAIN/"); ok {
			return sayAgainIntent(what)
		} else if speedStr, untilStr, ok := strings.Cut(command[1:], "/U"); ok {
			// Check for compound format: S250/UFIX1/210/UFIX2/180
			// After the first Cut on "/U", if untilStr contains another "/",
			// it's compound. This works because current single speed-until
			// specs (fix names, numbers, NdME) never contain "/".
			if strings.Contains(untilStr, "/") {
				segments, err := parseCompoundSpeed(command[1:])
				if err != nil {
					return nil, err
				}
				return s.AssignCompoundSpeed(tcw, callsign, segments)
			}
			sr, err := parseSpeedRestriction(speedStr)
			if err != nil {
				return nil, err
			}
			until := parseSpeedUntil(untilStr)
			return s.AssignSpeedUntil(tcw, callsign, sr, until)
		} else {
			sr, err := parseSpeedRestriction(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignSpeed(tcw, callsign, sr, false)
		}

	case 'T':
		if command == "TRAFFIC" {
			return s.TrafficInSightInquiry(tcw, callsign)
		} else if command == "TRAFFIC/INSIGHT" {
			return s.TrafficHasYouInSight(tcw, callsign)
		} else if trafficSpec, ok := strings.CutPrefix(command, "TRAFFIC/"); ok {
			// Parse the command: TRAFFIC/oclock/miles/altitude
			// Altitude may be the literal "UNK" if the controller said
			// "altitude unknown".
			args := strings.Split(trafficSpec, "/")
			if len(args) != 3 {
				return nil, ErrInvalidCommandSyntax
			}

			oclock, err := parseNumber(args[0])
			if err != nil || oclock < 1 || oclock > 12 {
				return nil, ErrInvalidCommandSyntax
			}

			miles, err := parseNumber(args[1])
			if err != nil || miles < 1 {
				return nil, ErrInvalidCommandSyntax
			}

			altUnknown := args[2] == "UNK"
			var trafficAlt int
			if !altUnknown {
				trafficAlt, err = parseNumber(args[2])
				if err != nil {
					return nil, ErrInvalidCommandSyntax
				}
			}

			return s.TrafficAdvisory(tcw, callsign, oclock, miles, trafficAlt*100, altUnknown)
		} else if command == "TO" {
			return s.ContactTower(tcw, callsign, av.Frequency(0))
		} else if f, ok := strings.CutPrefix(command, "TO/"); ok {
			var freq av.Frequency
			if n, err := parseNumber(f); err == nil {
				freq = av.Frequency(n)
			}
			return s.ContactTower(tcw, callsign, freq)
		} else if n := len(command); n > 2 {
			if deg, err := parseNumber(command[1 : n-1]); err == nil {
				if command[n-1] == 'L' {
					return s.AssignHeading(&HeadingArgs{
						TCW:            tcw,
						ADSBCallsign:   callsign,
						LeftDegrees:    deg,
						DelayReduction: delayReduction,
					})
				} else if command[n-1] == 'R' {
					return s.AssignHeading(&HeadingArgs{
						TCW:            tcw,
						ADSBCallsign:   callsign,
						RightDegrees:   deg,
						DelayReduction: delayReduction,
					})
				}
			}

			switch command[:2] {
			case "TS":
				sr, err := parseSpeedRestriction(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignSpeed(tcw, callsign, sr, true)
			case "TM":
				sr, err := parseSpeedRestriction(command[1:])
				if err != nil {
					return nil, err
				}
				return s.AssignSpeed(tcw, callsign, sr, true)
			case "TA", "TC", "TD":
				alt, err := parseNumber(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignAltitude(tcw, callsign, 100*alt, true, delayReduction)

			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'V':
		if command == "VISSEP" {
			return s.MaintainVisualSeparation(tcw, callsign)
		}
		return nil, ErrInvalidCommandSyntax

	case 'X':
		s.DeleteAircraft(tcw, callsign)
		return nil, nil // DeleteAircraft returns no intent

	default:
		return nil, ErrInvalidCommandSyntax
	}
}
