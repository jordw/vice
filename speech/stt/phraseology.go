// speech/stt/phraseology.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// phraseology is the grammar of what a controller says to an aircraft,
// together with the rules that stand for whole instructions.
type phraseology struct {
	*grammar
	rules []*rule
}

// thePhraseology returns the phraseology, built on first use.
var thePhraseology = sync.OnceValue(func() *phraseology {
	p := &phraseology{grammar: newGrammar()}
	p.defineSlots()
	p.defineSpeedRules()
	p.defineAltitudeRules()
	p.defineHeadingRules()
	p.defineRouteRules()
	p.defineApproachRules()
	p.defineRadioRules()
	p.defineAdvisoryRules()
	p.defineDiscourseRules()
	return p
})

// command adds a rule for an instruction. build returns the command, or
// several separated by spaces, or "" if the reading is not acceptable.
func (p *phraseology) command(template string, build func(c captured) string) *rule {
	prod := p.define("instruction", template, func(c captured) any {
		if s := build(c); s != "" {
			return s
		}
		return nil
	})
	r := &rule{production: prod}
	p.rules = append(p.rules, r)
	return r
}

// remark adds a rule for words that are understood but that give the
// pilot nothing to do.
func (p *phraseology) remark(kind ruleKind, template string) *rule {
	prod := p.define("instruction", template, func(captured) any { return "" })
	r := &rule{production: prod, kind: kind}
	p.rules = append(p.rules, r)
	return r
}

// incompleteScale is the fraction of the evidence for the opening words
// of an instruction that counts when its value was not understood. It is
// small so that any complete reading of the same words wins.
const incompleteScale = 0.5

// in sets the rule's category.
func (r *rule) in(c category) *rule {
	r.category = c
	return r
}

// then marks the rule as one whose command has a "then" form.
func (r *rule) then() *rule {
	r.sequenced = true
	return r
}

// scaled multiplies the evidence of the rule's readings.
func (r *rule) scaled(s float32) *rule {
	r.scale = s
	return r
}

// orSayAgain adds a companion rule that matches the words of the
// template before its first value and asks the pilot to have that value
// said again. what names the value: "ALTITUDE", "HEADING".
func (p *phraseology) orSayAgain(r *rule, what string) *rule {
	// The opening words run up to the first slot that is not optional.
	depth, end := 0, -1
	for i, c := range r.template {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case '{':
			if depth == 0 && end < 0 {
				end = i
			}
		}
	}
	if end <= 0 {
		panic(fmt.Sprintf("stt: template %q has no opening words", r.template))
	}
	prod := p.define("instruction", strings.TrimSpace(r.template[:end]), func(captured) any { return "SAYAGAIN/" + what })
	prod.scale = incompleteScale
	p.rules = append(p.rules, &rule{production: prod, incomplete: true, category: r.category})
	return r
}

// defineSlots registers the slots that read values from the transcript.
func (p *phraseology) defineSlots() {
	number := func(name string, interpret func(d *decoding, r numberReading) (quantity, bool)) {
		p.slot(name, func(d *decoding, pos int) []match {
			return d.readQuantity(pos, func(r numberReading) (quantity, bool) { return interpret(d, r) })
		})
	}
	ranged := func(name string, lo, hi int) {
		interpret := interpretRange(lo, hi)
		number(name, func(_ *decoding, r numberReading) (quantity, bool) { return interpret(r) })
	}

	number("heading", func(_ *decoding, r numberReading) (quantity, bool) { return interpretHeading(r) })
	number("full_heading", func(_ *decoding, r numberReading) (quantity, bool) {
		if len(r.digits) != 3 {
			return quantity{}, false
		}
		return interpretHeading(r)
	})
	number("speed", func(d *decoding, r numberReading) (quantity, bool) { return d.interpretSpeed(r) })
	number("mach", func(_ *decoding, r numberReading) (quantity, bool) { return interpretMach(r) })
	number("flight_level", func(_ *decoding, r numberReading) (quantity, bool) { return interpretFlightLevel(r) })
	ranged("clock", 1, 12)
	ranged("miles", 1, 99)
	ranged("final_miles", lowestFinalMiles, highestFinalMiles)
	p.slot("vague_final_miles", (*decoding).readVagueFinalMiles)
	ranged("runway_number", 1, 36)
	ranged("degrees", 1, 45)
	ranged("minutes", 1, 10)
	ranged("digit", 1, 9)

	// An altitude where three bare digits are a flight level, and one
	// where they would be a speed.
	p.slot("any_altitude", func(d *decoding, pos int) []match { return d.readAltitude(pos, true) })
	p.slot("low_altitude", func(d *decoding, pos int) []match { return d.readAltitude(pos, false) })
	p.slot("thousands", func(d *decoding, pos int) []match { return d.readThousands(pos, false) })
	p.slot("squawk", (*decoding).readSquawk)
	p.slot("frequency", (*decoding).readFrequency)
	p.slot("altimeter", (*decoding).readAltimeter)
	p.slot("altimeter_setting", (*decoding).readAltimeterSetting)
	p.slot("unknown_name", (*decoding).readUnknownName)
	p.slot("fix", (*decoding).readFix)
	p.slot("expected_fix", (*decoding).readExpectedFix)
	p.slot("unknown_word", (*decoding).readUnknownWord)
	p.slot("approach", (*decoding).readApproach)
	p.slot("named_approach", (*decoding).readNamedApproach)
	p.slot("visual_runway", (*decoding).readVisualRunway)
	p.slot("lahso_runway", (*decoding).readLAHSORunway)
	p.slot("letter", (*decoding).readLetter)
	p.slot("airport", (*decoding).readAirport)
	p.slot("reporting_point", (*decoding).readReportingPoint)
	p.slot("sid", readSID)
	p.slot("star", readSTAR)
	p.slot("compass", (*decoding).readCompass)
	p.slot("airway", (*decoding).readAirway)
	p.slot("aircraft_type", (*decoding).readAircraftType)
	p.slot("any_word", (*decoding).readAnyWord)
	p.slot("facility_word", (*decoding).readFacilityWord)
	p.slot("facility_frequency", (*decoding).readFacilityFrequency)
	p.slot("traffic", (*decoding).readTraffic)
	p.slot("traffic_in_sight", (*decoding).readTrafficInSight)

	// Altitudes are also spoken with "flight level" in front.
	level := func(c captured) any { return c.int(0) }
	for _, name := range []string{"altitude", "altitude_fl"} {
		p.define(name, "flight level {flight_level}", level)
		p.define(name, "fl {flight_level}", level)
	}
	p.define("altitude", "{low_altitude} [feet]", level)
	p.define("altitude_fl", "{any_altitude} [feet]", level)

	p.define("turns", "left [turns|turn]", func(captured) any { return "L" })
	p.define("turns", "right [turns|turn]", func(captured) any { return "R" })
	// The request to look for the traffic, which ends an advisory.
	p.define("traffic_report", "report|reported [it|him|them|that|traffic] [in] sight|set", func(captured) any { return "" })
	p.define("traffic_report", "[do] [you] have it|him|them|that|traffic [in] sight|set", func(captured) any { return "" })
	// The other aircraft sees this one and keeps clear of it.
	p.define("other_has_us", "[has|have] you in sight [and] [will] maintain visual separation", func(captured) any { return "" })
}

// altitudeTolerance is how far, in feet, an aircraft can be on the wrong
// side of an altitude it is told to climb or descend to before the
// instruction is taken to be misheard.
const altitudeTolerance = 500

// contradictedMarker starts the result of a climb or descent that the
// aircraft's altitude contradicts and whose verb was not heard exactly.
// The altitude in hundreds of feet follows it.
const contradictedMarker = "CONTRADICTED/"

// descendTo returns the command for a descent to an altitude. If the
// aircraft is already below it, something was misheard. When "descend"
// was heard as such, it is the altitude. Otherwise the words around the
// instruction decide; see contradictedMarker.
func descendTo(c captured, alt int) string {
	if alt*100 > c.d.aircraft.Altitude+altitudeTolerance {
		if !c.said("descend", "descending", "descended") {
			return fmt.Sprintf("%s%d", contradictedMarker, alt)
		}
		return "SAYAGAIN/ALTITUDE"
	}
	return fmt.Sprintf("D%d", alt)
}

// climbTo returns the command for a climb to an altitude; see descendTo.
func climbTo(c captured, alt int) string {
	if alt*100 < c.d.aircraft.Altitude-altitudeTolerance {
		if !c.said("climb", "climbing", "climbed") {
			return fmt.Sprintf("%s%d", contradictedMarker, alt)
		}
		return "SAYAGAIN/ALTITUDE"
	}
	return fmt.Sprintf("C%d", alt)
}

func (p *phraseology) defineAltitudeRules() {
	descend := func(c captured) string { return descendTo(c, c.int(0)) }
	climb := func(c captured) string { return climbTo(c, c.int(0)) }

	p.orSayAgain(p.command("descend|descending|descended [and] maintain {altitude_fl}", descend).in(categoryAltitude).then(),
		"ALTITUDE")
	p.orSayAgain(p.command("descend|descending|descended [and] [to] {altitude_fl}", descend).in(categoryAltitude).then(),
		"ALTITUDE")
	p.orSayAgain(p.command("climb|climbing|climbed [and] maintain {altitude_fl}", climb).in(categoryAltitude).then(),
		"ALTITUDE")
	p.command("climb|climbing|climbed [and] [to] {altitude_fl}", climb).in(categoryAltitude).then()

	// A number before "knots" is a speed.
	maintain := func(c captured) string {
		if c.d.text(c.end) == "knots" {
			return ""
		}
		return fmt.Sprintf("A%d", c.int(0))
	}
	p.command("maintain [at] {altitude}", maintain).in(categoryAltitude).then()
	// "Level" alone marks a flight level.
	p.command("maintain level {flight_level}", maintain).in(categoryAltitude)

	// An altitude said in thousands needs no verb to be understood, and
	// one before "until established" belongs to an approach clearance.
	established := " until|till established|establishing [on|from|onto|of] [the] [localizer|glideslope|glide|slope|final] [approach] [course]"
	p.command("{thousands} [feet]", maintain).in(categoryAltitude)
	p.command("{thousands} [feet]"+established, maintain).in(categoryAltitude)
	p.command("maintain [at] {altitude}"+established, maintain).in(categoryAltitude)
	p.remark(kindInformational, established[1:])
	p.remark(kindInformational, "expect further clearance")

	// Expedite, and its synonym "best rate".
	for _, verb := range []string{"expedite", "best rate"} {
		p.command(verb+" descent|descend [through] {altitude_fl}", func(c captured) string {
			return fmt.Sprintf("ED%d", c.int(0))
		})
		p.command(verb+" climb [through] {altitude_fl}", func(c captured) string { return fmt.Sprintf("EC%d", c.int(0)) })
		p.command(verb+" descent|descend", func(c captured) string {
			if ac := c.d.aircraft; ac.State == stateDeparture && len(ac.CandidateApproaches) == 0 {
				return ""
			}
			return "ED"
		})
		p.command(verb+" climb", func(c captured) string {
			if s := c.d.aircraft.State; s == stateArrival || s == stateClearedApproach {
				return ""
			}
			return "EC"
		})
	}
	// "Expedite" alone takes its direction from the altitude instruction
	// before it; see resolveExpedite.
	p.command("expedite", func(c captured) string {
		if t := c.d.text(c.end); t == "climb" || t == "descent" || t == "descend" {
			return ""
		}
		return expediteMarker
	})

	p.command("good rate [of] descent|descend", func(captured) string { return "GRD" })
	p.command("good rate [of] climb", func(captured) string { return "GRC" })
	p.command("good rate [through] {altitude_fl}", func(c captured) string { return fmt.Sprintf("GR%d", c.int(0)) })

	p.command("say altitude", func(captured) string { return "SA" })
	p.command("altitude [at] [your] discretion", func(c captured) string {
		if c.d.aircraft.State != stateVFR {
			return ""
		}
		return "A"
	})

	p.defineProcedureRules()
}

// defineProcedureRules adds climbing via a SID and descending via a STAR,
// with their "except maintain" qualifiers (7110.65 4-5-7 and 5-7-2).
func (p *phraseology) defineProcedureRules() {
	// The exceptions: an altitude is an argument of the via command, and a
	// speed is the same instruction as a separate speed assignment.
	type exception struct{ altitude, speed string }
	altitude := func(c captured) any { return exception{altitude: fmt.Sprintf("/A%d", c.int(0))} }
	speed := func(c captured) any { return exception{speed: " " + c.str(0)} }
	both := func(alt, spd int) func(c captured) any {
		return func(c captured) any {
			return exception{altitude: fmt.Sprintf("/A%d", c.int(alt)), speed: " " + c.str(spd)}
		}
	}
	p.define("except", "except maintain {altitude_fl}", altitude)
	p.define("except", "except {speed_limit}", speed)
	p.define("except", "except maintain {altitude_fl} [and] {speed_limit}", both(0, 1))
	p.define("except", "except {speed_limit} [and] [maintain] {altitude_fl}", both(1, 0))

	via := func(cmd string, allowed func(ac *Aircraft, alt int) bool) func(c captured) string {
		return func(c captured) string {
			if !c.has(1) {
				if !allowed(c.d.aircraft, -1) {
					return ""
				}
				return cmd
			}
			ex := c.values[1].(exception)
			if ex.altitude != "" {
				var alt int
				fmt.Sscanf(ex.altitude, "/A%d", &alt)
				if !allowed(c.d.aircraft, alt) {
					return "SAYAGAIN/ALTITUDE" + ex.speed
				}
			}
			return cmd + ex.altitude + ex.speed
		}
	}
	// via0 adapts a via command to a template that names no procedure.
	via0 := func(build func(c captured) string) func(c captured) string {
		return func(c captured) string {
			c.values = append([]any{""}, c.values...)
			return build(c)
		}
	}
	climbVia := via("CVS", func(ac *Aircraft, alt int) bool {
		return ac.State == stateDeparture && (alt < 0 || alt*100 >= ac.Altitude-altitudeTolerance)
	})
	descendVia := via("DVS", func(ac *Aircraft, alt int) bool {
		return alt < 0 || alt*100 <= ac.Altitude+altitudeTolerance
	})

	p.command("climb|climbing via [the] {sid} [departure] [{except}]", climbVia).in(categoryAltitude)
	p.command("climb|climbing via [the] sid [{except}]", via0(climbVia)).in(categoryAltitude)
	p.command("descend|descending via [the] {star} [arrival] [{except}]", descendVia).in(categoryAltitude)
	p.command("descend|descending via [the] star|stars|arrival [{except}]", via0(descendVia)).in(categoryAltitude)
	// "Via the (STAR) arrival" says enough even when "descend" is lost.
	p.command("via [the] {star} arrival [{except}]", descendVia).in(categoryAltitude)
	// The name of the procedure, not understood. An altitude after it can
	// only be the one to maintain.
	p.command("descend|descending via [the] {unknown_name} arrival [{except}]", descendVia).in(categoryAltitude)
	p.command("climb|climbing via [the] {unknown_name} [departure] {thousands}", func(c captured) string {
		c.values[1] = exception{altitude: fmt.Sprintf("/A%d", c.int(1))}
		return climbVia(c)
	}).in(categoryAltitude)
}

// speedCommand returns the command for a speed assignment. steps is the
// assignment as it appears after the "S": "250", "210+/U5DME". A first
// speed far below what the aircraft can fly was misheard.
func speedCommand(c captured, steps string) string {
	first := steps[:strings.IndexFunc(steps+"/", func(r rune) bool { return r < '0' || r > '9' })]
	if spd, err := strconv.Atoi(first); err == nil {
		if lowest := c.d.aircraft.minimumSpeed(); lowest > 0 && 4*spd < 3*lowest {
			return "SAYAGAIN/SPEED"
		}
	}
	return "S" + steps
}

func (p *phraseology) defineSpeedRules() {
	// A speed, possibly as a limit.
	p.define("speed_value", "{speed} [knots]", func(c captured) any { return strconv.Itoa(c.int(0)) })
	p.define("speed_value", "{speed} [knots] or greater|better", func(c captured) any { return strconv.Itoa(c.int(0)) + "+" })
	p.define("speed_value", "{speed} [knots] or less", func(c captured) any { return strconv.Itoa(c.int(0)) + "-" })

	// Where a speed assignment ends: at a fix, at a distance from the
	// runway.
	p.define("until_fix", "until|till|til|unto [the] {expected_fix}", func(c captured) any { return "/U" + c.str(0) })
	p.define("until", "until|till|til|unto [the] {expected_fix}", func(c captured) any { return "/U" + c.str(0) })
	p.define("until", "until|till|til|unto|to [the|a] {miles} dme", func(c captured) any {
		return fmt.Sprintf("/U%dDME", c.int(0))
	})
	finalMiles := func(c captured) any { return fmt.Sprintf("/U%d", c.int(0)) }
	p.define("until", "until|till|til|unto|to [the|a] {final_miles} mile|miles final", finalMiles)
	p.define("until", "until|till|til|unto [the|a] {vague_final_miles} mile|miles final", finalMiles)
	p.define("until", "until|till|til|unto [the|a] {final_miles}", func(c captured) any {
		return fmt.Sprintf("/U%d", c.int(0))
	})
	p.define("until", "to {fix}", func(c captured) any { return "/U" + c.str(0) })
	p.define("until", "until|till|til|unto [the|a] {final_miles} mile|miles correction {final_miles} [mile|miles] [final]",
		func(c captured) any { return fmt.Sprintf("/U%d", c.int(1)) })

	// A speed assignment: one speed, or several that each hold until a
	// fix.
	p.define("speed_steps", "{speed_value}", func(c captured) any { return c.str(0) })
	p.define("speed_steps", "{speed_value} {until}", func(c captured) any { return c.str(0) + c.str(1) })
	p.define("speed_steps", "{speed_value} {until_fix} [then] [speed] [to] {speed_steps}", func(c captured) any {
		return c.str(0) + c.str(1) + "/" + c.str(2)
	})
	// "Until advised" and "for now" name no end.
	p.define("speed_steps", "{speed_value} until|till advised|further [notice]", func(c captured) any { return c.str(0) })
	p.define("speed_steps", "{speed_value} for now", func(c captured) any { return c.str(0) })

	assign := func(c captured) string { return speedCommand(c, c.str(0)) }
	p.command("reduce|slow|increase [speed] [to] {speed_steps}", assign).in(categorySpeed).then()
	p.command("speed [to] {speed_steps}", assign).in(categorySpeed).then()
	p.orSayAgain(p.command("maintain [speed] {speed_steps}", assign).in(categorySpeed).then(), "SPEED")
	p.command("do not exceed {speed} [knots]", func(c captured) string {
		return speedCommand(c, strconv.Itoa(c.int(0))+"-")
	}).in(categorySpeed).then()
	// A speed that is marked as one needs no verb.
	p.command("{speed} knots [or greater|better|less] {until}", func(c captured) string {
		mod := ""
		if c.said("greater", "better") {
			mod = "+"
		} else if c.said("less") {
			mod = "-"
		}
		return speedCommand(c, strconv.Itoa(c.int(0))+mod+c.str(1))
	}).in(categorySpeed)
	p.command("{speed} [knots] or greater|better [{until}]", func(c captured) string {
		steps := strconv.Itoa(c.int(0)) + "+"
		if c.has(1) {
			steps += c.str(1)
		}
		return speedCommand(c, steps)
	}).in(categorySpeed).then()
	p.command("{speed} [knots] or less [{until}]", func(c captured) string {
		steps := strconv.Itoa(c.int(0)) + "-"
		if c.has(1) {
			steps += c.str(1)
		}
		return speedCommand(c, steps)
	}).in(categorySpeed).then()
	// "Speed eight": a value was said but is not a speed.
	p.command("speed [to] {digit}", func(captured) string { return "SAYAGAIN/SPEED" }).in(categorySpeed)
	// "Have you maintained two two zero knots" assigns the speed.
	p.command("maintained {speed} knots", func(c captured) string { return speedCommand(c, strconv.Itoa(c.int(0))) }).in(categorySpeed)
	// "Maintain that until Dennis" extends the speed just assigned; see
	// mergeUntilFix.
	p.command("maintain that|it until {fix}", func(c captured) string { return untilFixMarker + c.str(0) })

	p.command("[maintain] slowest|minimum practical|possible|approach [speed]", func(captured) string { return "SMIN" }).in(categorySpeed)
	p.command("[maintain] minimum speed", func(captured) string { return "SMIN" }).in(categorySpeed)
	p.command("reduce|slow [to] [your] final|minimum [approach] [speed]", func(captured) string { return "SMIN" }).in(categorySpeed)
	p.command("[maintain] maximum|best forward [speed]", func(captured) string { return "SMAX" }).in(categorySpeed)
	p.command("[maintain] maximum speed", func(captured) string { return "SMAX" }).in(categorySpeed)
	p.command("[maintain] present speed", func(captured) string { return "SPRES" }).in(categorySpeed)

	cancel := func(captured) string { return "S" }
	p.command("cancel speed [restrictions|restriction]", cancel).in(categorySpeed)
	p.command("resume normal speed", cancel).in(categorySpeed)
	p.command("speed [at] [your] discretion", cancel).in(categorySpeed)
	p.command("comply [with] speed restrictions", cancel).in(categorySpeed)
	p.command("delete|no speed restrictions|restriction", cancel).in(categorySpeed)

	p.command("say speed|airspeed", func(captured) string { return "SS" })
	p.command("say indicated [speed|airspeed]", func(captured) string { return "SI" })
	p.command("say mach [number]", func(captured) string { return "SM" })

	// Mach numbers.
	p.define("mach_value", "mach [point] {mach}", func(c captured) any { return strconv.Itoa(c.int(0)) })
	p.define("mach_value", "mach [point] {mach} or greater|better", func(c captured) any { return strconv.Itoa(c.int(0)) + "+" })
	p.define("mach_value", "mach [point] {mach} or less", func(c captured) any { return strconv.Itoa(c.int(0)) + "-" })
	mach := func(c captured) string { return "M" + c.str(0) }
	p.command("[maintain] {mach_value}", mach).in(categorySpeed)
	p.command("reduce|slow|increase [speed] [to] {mach_value}", mach).in(categorySpeed)
	p.command("do not exceed mach [point] {mach}", func(c captured) string { return fmt.Sprintf("M%d-", c.int(0)) }).in(categorySpeed)

	// A speed or Mach limit as part of a longer instruction: a crossing
	// restriction, or an exception to a procedure.
	p.define("speed_limit", "[maintain] [at] {speed_value}", func(c captured) any { return "S" + c.str(0) })
	p.define("speed_limit", "[at] [or] [and] [do] not [to] exceed {speed} [knots]", func(c captured) any {
		return fmt.Sprintf("S%d-", c.int(0))
	})
	p.define("speed_limit", "[maintain] [at] {mach_value}", func(c captured) any { return "M" + c.str(0) })
}
