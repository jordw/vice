// speech/stt/phraseology_routes.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func (p *phraseology) defineHeadingRules() {
	turn := func(dir string) func(c captured) string {
		return func(c captured) string { return fmt.Sprintf("%s%03d", dir, c.int(0)) }
	}
	p.orSayAgain(p.command("[turn] [to] left [turn] heading {heading}", turn("L")).in(categoryHeading), "HEADING")
	p.orSayAgain(p.command("[turn] [to] right [turn] heading {heading}", turn("R")).in(categoryHeading), "HEADING")
	p.command("[turn] [to] left [turn] {heading}", turn("L")).in(categoryHeading)
	p.command("[turn] [to] right [turn] {heading}", turn("R")).in(categoryHeading)
	p.orSayAgain(p.command("fly heading {heading}", turn("H")).in(categoryHeading), "HEADING")
	p.orSayAgain(p.command("heading {heading}", turn("H")).in(categoryHeading), "HEADING")
	p.command("flying {heading}", turn("H")).in(categoryHeading)
	p.command("fly {full_heading}", turn("H")).in(categoryHeading)
	// "Turn two seven zero": the direction or "heading" was lost. Only a
	// heading said in full is taken, so that a stray number is not one.
	p.command("turn|turning [to] {full_heading}", turn("H")).in(categoryHeading)

	present := func(captured) string { return "H" }
	p.command("[fly] present heading", present).in(categoryHeading)
	p.command("maintain|continue present|current heading", present).in(categoryHeading)
	p.command("fly present", present).in(categoryHeading)
	p.command("say heading", func(captured) string { return "SH" })

	// A turn by a number of degrees, rounded to the fives controllers use.
	degrees := func(dir string) func(c captured) string {
		return func(c captured) string { return fmt.Sprintf("T%d%s", max(5, (c.int(0)+2)/5*5), dir) }
	}
	p.command("[turn] {degrees} degrees|degree [to] [the] left", degrees("L")).in(categoryHeading)
	p.command("[turn] {degrees} degrees|degree [to] [the] right", degrees("R")).in(categoryHeading)
	p.command("turn {degrees} left", degrees("L")).in(categoryHeading)
	p.command("turn {degrees} right", degrees("R")).in(categoryHeading)
	p.command("[turn] left {degrees} degrees|degree", degrees("L")).in(categoryHeading)
	p.command("[turn] right {degrees} degrees|degree", degrees("R")).in(categoryHeading)

	// The reason for a vector is not an instruction.
	p.remark(kindCommand, "vectors|vector [for] [the] sequence|spacing|final|traffic")
}

// reciprocal returns the opposite of a magnetic course, with 360 for
// north as in the commands.
func reciprocal(course int) int {
	if r := int(math.OppositeHeading(float32(course))); r != 0 {
		return r
	}
	return 360
}

func (p *phraseology) defineRouteRules() {
	direct := func(prefix string) func(c captured) string {
		return func(c captured) string { return prefix + c.str(0) }
	}
	p.command("[proceed] left [turn] direct [to] [at] {expected_fix}", direct("LD")).in(categoryRoute)
	p.command("[proceed] right [turn] direct [to] [at] {expected_fix}", direct("RD")).in(categoryRoute)
	p.command("direct|proceed [direct] [to] [at] {expected_fix}", direct("D")).in(categoryRoute)
	p.command("cleared [to] [at] {expected_fix}", direct("D")).in(categoryRoute)
	p.orSayAgain(p.command("cleared direct [to] [at] {expected_fix}", direct("D")).in(categoryRoute), "FIX")
	// A word where the fix belongs that is no fix the aircraft knows.
	sayAgainFix := func(captured) string { return "SAYAGAIN/FIX" }
	p.command("direct|proceed [direct] [to] {unknown_word}", sayAgainFix).scaled(incompleteScale).incomplete = true
	p.command("cleared direct [to] {unknown_word}", sayAgainFix).scaled(incompleteScale).incomplete = true

	p.command("expect [to] [go] direct [to] {fix}", direct("EXPDIR"))
	p.command("expect [to] rejoin|resume [the] arrival [at] {fix}", direct("EXPDIR"))

	// Crossing restrictions. An altitude can be exact, a floor or a
	// ceiling, and a speed can come with it in either order.
	p.define("crossing_altitude", "[at] [and] [maintain] {altitude}", func(c captured) any { return fmt.Sprintf("A%d", c.int(0)) })
	p.define("crossing_altitude", "[at] [or] above {altitude}", func(c captured) any { return fmt.Sprintf("A%d+", c.int(0)) })
	p.define("crossing_altitude", "[at] [or] below {altitude}", func(c captured) any { return fmt.Sprintf("A%d-", c.int(0)) })
	p.define("crossing", "{crossing_altitude}", func(c captured) any { return "/" + c.str(0) })
	p.define("crossing", "{speed_limit}", func(c captured) any { return "/" + c.str(0) })
	p.define("crossing", "{crossing_altitude} [and] {speed_limit}", func(c captured) any {
		return "/" + c.str(0) + "/" + c.str(1)
	})
	p.define("crossing", "{speed_limit} [and] {crossing_altitude}", func(c captured) any {
		return "/" + c.str(1) + "/" + c.str(0)
	})
	p.command("cross {fix} {crossing}", func(c captured) string { return "C" + c.str(0) + c.str(1) })
	p.command("cross {miles} miles|mile {compass} [of] {fix} {crossing}", func(c captured) string {
		return fmt.Sprintf("C%s/%d%s%s", c.str(2), c.int(0), c.str(1), c.str(3))
	})
	// A distance from the runway of a visual approach.
	p.command("cross {miles} dme {crossing_altitude}", func(c captured) string {
		return fmt.Sprintf("CDME%d/%s", c.int(0), c.str(1))
	})

	p.command("depart {fix} [heading] {heading}", func(c captured) string {
		return fmt.Sprintf("D%s/H%03d", c.str(0), c.int(1))
	})

	p.command("hold [north|south|east|west|northeast|northwest|southeast|southwest] [of] [at] {fix} [as] [published]",
		func(c captured) string { return "H" + c.str(0) })
	p.command("hold [north|south|east|west|northeast|northwest|southeast|southwest] [of] [at] {fix} [on] [the] {heading} radial|bearing [inbound] [{minutes} minute [legs|leg]] [{turns}]",
		func(c captured) string {
			cmd := fmt.Sprintf("H%s/R%d", c.str(0), c.int(1))
			if c.has(2) {
				cmd += fmt.Sprintf("/%dM", c.int(2))
			}
			if c.has(3) {
				return cmd + "/" + c.str(3)
			}
			return cmd + "/R"
		})

	// Intercepting a radial (7110.65 2-5-2): a radial or bearing is
	// measured from the fix and a course to it, and inbound is the default.
	p.define("radial_direction", "[and] [then] [track] [proceed] inbound", func(captured) any { return "" })
	p.define("radial_direction", "[and] [then] [track] [proceed] outbound", func(captured) any { return "O" })
	radial := func(fix, radial, dir int) func(c captured) string {
		return func(c captured) string {
			cmd := fmt.Sprintf("I%s/%03d", c.str(fix), c.int(radial))
			if c.has(dir) {
				cmd += c.str(dir)
			}
			return cmd
		}
	}
	p.orSayAgain(p.command("intercept|join the {fix} {heading} radial|bearing [{radial_direction}]", radial(0, 1, 2)).in(categoryRoute),
		"FIX")
	p.command("intercept|join {fix} {heading} radial|bearing [{radial_direction}]", radial(0, 1, 2)).in(categoryRoute)
	p.command("intercept|join [the] {heading} radial|bearing [from|off|of] {fix} [{radial_direction}]", radial(1, 0, 2)).in(categoryRoute)
	p.command("intercept|join [the] {heading} course [to|inbound] [to] {fix}", func(c captured) string {
		return fmt.Sprintf("I%s/%03d", c.str(1), reciprocal(c.int(0)))
	}).in(categoryRoute)

	// Joining an airway (7110.65 4-4-1) and resuming a procedure after a
	// vector (5-6-2).
	p.orSayAgain(p.command("join|intercept [the] {airway} [airway]", func(c captured) string { return "J" + c.str(0) }).in(categoryRoute),
		"AIRWAY")
	p.command("resume|rejoin|join|intercept [the] {sid} [departure]", func(captured) string { return "RSID" }).in(categoryRoute)
	p.command("resume|rejoin|join|intercept [the] {star} [arrival]", func(captured) string { return "RSTAR" }).in(categoryRoute)
	p.command("resume|rejoin|join [the] sid", func(captured) string { return "RSID" }).in(categoryRoute)
	p.command("resume|rejoin|join [the] star|arrival", func(captured) string { return "RSTAR" }).in(categoryRoute)
	p.command("resume own navigation", func(captured) string { return "RON" }).in(categoryRoute)

	// "Vectors to" something says why the vector is given (7110.65 5-6-2).
	p.remark(kindCommand, "vector|vectors [to] [join|intercept] [the] {fix} {heading} radial|bearing [{radial_direction}]")
	p.remark(kindCommand, "vector|vectors [to] [join|intercept] [the] {heading} radial|bearing [from|off|of] {fix} [{radial_direction}]")
	p.remark(kindCommand, "vector|vectors [to] [join|intercept] [the] {heading} course [to|inbound] [to] {fix}")
	p.remark(kindCommand, "vector|vectors [to] [join|intercept] [the] {airway} [airway]")
	p.remark(kindCommand, "vector|vectors [to] [resume|rejoin|join|intercept] [the] {sid} [departure]")
	p.remark(kindCommand, "vector|vectors [to] [resume|rejoin|join|intercept] [the] {star} [arrival]")

	// A heading given for a join that the aircraft was already told to
	// make.
	p.define("to_join", "to join|intercept", func(captured) any { return "/J" })
	p.define("to_join", "to join|intercept [the] localizer", func(captured) any { return " I" })
	join := func(dir string) func(c captured) string {
		return func(c captured) string { return fmt.Sprintf("%s%03d%s", dir, c.int(0), c.str(1)) }
	}
	p.command("[fly] heading {heading} {to_join}", join("H")).in(categoryHeading)
	p.command("make it [a] {heading} [heading] {to_join}", join("H")).in(categoryHeading)
	p.command("[turn] [to] left [heading] {heading} {to_join}", join("L")).in(categoryHeading)
	p.command("[turn] [to] right [heading] {heading} {to_join}", join("R")).in(categoryHeading)

	// Instructions that take effect at a fix.
	p.command("after|at {fix} {speed_limit}", func(c captured) string { return "A" + c.str(0) + "/" + c.str(1) })
	p.command("after|at {fix} reduce|slow|increase [speed] [to] {speed_value}", func(c captured) string {
		return "A" + c.str(0) + "/S" + c.str(1)
	})
	p.command("after|at {fix} climb [and] maintain {altitude_fl}", func(c captured) string {
		return fmt.Sprintf("A%s/C%d", c.str(0), c.int(1))
	})
	p.command("after|at {fix} descend [and] maintain {altitude_fl}", func(c captured) string {
		return fmt.Sprintf("A%s/D%d", c.str(0), c.int(1))
	})
	// An altitude said in thousands needs no verb here either: the
	// aircraft's altitude says which way it goes.
	p.command("after|at {fix} {thousands}", func(c captured) string {
		if c.int(1)*100 > c.d.aircraft.Altitude {
			return fmt.Sprintf("A%s/C%d", c.str(0), c.int(1))
		}
		return fmt.Sprintf("A%s/D%d", c.str(0), c.int(1))
	})
}

// impliedScale is the fraction of the evidence for an approach name that
// counts when no verb says what to do with it.
const impliedScale = 0.3

func (p *phraseology) defineApproachRules() {
	// "Land and hold short of runway three one left" after an approach.
	p.define("lahso", "[land] [and] hold short [of] [runway] {lahso_runway}", func(c captured) any {
		return "/LAHSO" + c.str(0)
	})
	withLAHSO := func(c captured, cmd string, i int) string {
		if c.has(i) {
			return cmd + c.str(i)
		}
		return cmd
	}

	expect := func(c captured) string { return withLAHSO(c, "E"+c.str(0), 1) }
	p.orSayAgain(p.command("expect [vectors] [for] [to] [the] {approach} [approach] [{lahso}]", expect).in(categoryApproachExpect),
		"APPROACH")
	p.command("vector|vectors [for] [to] [the] {approach} [approach] [{lahso}]", expect).in(categoryApproachExpect)

	expectVisual := func(c captured) string { return withLAHSO(c, "EVA"+c.str(0), 1) }
	p.command("expect [the|a] [vectors] [for] [to] [the] {visual_runway} [approach] [{lahso}]", expectVisual).in(categoryApproachExpect)
	p.command("vector|vectors [for] [to] [the] {visual_runway} [approach] [{lahso}]", expectVisual).in(categoryApproachExpect)

	// A clearance whose verb was not "cleared" as such, for an aircraft
	// that was told to expect no approach, is the "expect" that comes
	// first.
	clearance := func(c captured, id string) string {
		if c.d.aircraft.AssignedApproach == "" && !c.said("cleared", "clear") {
			return "E" + id
		}
		return "C" + id
	}
	cleared := func(c captured) string { return clearance(c, c.str(0)) }
	p.orSayAgain(p.command("cleared approach [for] [the] {approach} [approach]", cleared).in(categoryApproachClearance), "APPROACH")
	p.command("cleared [for] [the] {approach} [approach]", cleared).in(categoryApproachClearance)
	p.command("clear to|for [the] {approach} [approach]", cleared).in(categoryApproachClearance)
	p.command("expect cleared [approach] [for] [the] {approach} [approach]", cleared).in(categoryApproachClearance)
	p.command("cleared [for] [the] {visual_runway} [approach] [{lahso}]", func(c captured) string {
		return withLAHSO(c, clearance(c, "VA"+c.str(0)), 1)
	}).in(categoryApproachClearance)
	p.command("cancel [approach] clearance", func(captured) string { return "CAC" })
	p.remark(kindCommand, "standby [for] [the] approach")

	// An approach named with no verb: the clearance for the approach the
	// aircraft expects, and otherwise the approach to expect.
	p.command("{named_approach} [approach]", func(c captured) string {
		if c.d.assignedApproachID() == c.str(0) {
			return "C" + c.str(0)
		}
		return "E" + c.str(0)
	}).in(categoryApproachExpect).scaled(impliedScale)

	p.orSayAgain(p.command("at {fix} cleared|clear [for] [the] {approach} [approach]", func(c captured) string {
		return "A" + c.str(0) + "/C" + c.str(1)
	}).in(categoryApproachClearance), "APPROACH")
	p.command("at {fix} [cleared] [clear] straight [in] [for] [the] {approach} [approach]", func(c captured) string {
		return "A" + c.str(0) + "/CSI" + c.str(1)
	}).in(categoryApproachClearance)
	p.command("at {fix} {approach} [approach]", func(c captured) string {
		return "A" + c.str(0) + "/C" + c.str(1)
	}).in(categoryApproachClearance)

	// Intercepting the final approach course.
	p.define("final_course", "[the] [runway] [{runway_number} [left|right|center]] localizer", func(captured) any { return "" })
	p.define("final_course", "[the] left|right|center localizer", func(captured) any { return "" })
	p.define("final_course", "[the] localizer [runway] {runway_number} [left|right|center]", func(captured) any { return "" })
	p.define("final_course", "[the] [runway] [{runway_number} [left|right|center]] final approach [course]",
		func(captured) any { return "" })
	p.define("final_course", "[the] [runway] [{runway_number} [left|right|center]] approach course", func(captured) any { return "" })
	p.command("intercept|join|set {final_course}", func(captured) string { return "I" })
	p.command("localizer", func(captured) string { return "I" })
	p.remark(kindCommand, "vectors|vector [to] [the] [for] [through] localizer")
	p.command("at {fix} intercept|join {final_course}", func(c captured) string { return fmt.Sprintf("A%s/I", c.str(0)) })
	p.command("direct|proceed [direct] [to] {fix} intercept|join {final_course}", func(c captured) string {
		return fmt.Sprintf("D%s A%s/I", c.str(0), c.str(0))
	}).in(categoryRoute)

	// A visual approach with a crossing restriction at a distance from the
	// runway: the clearance goes first so that the restriction applies to
	// it.
	p.command("cross {miles} dme {crossing_altitude} cleared [for] [the] {visual_runway} [approach]", func(c captured) string {
		return fmt.Sprintf("CVA%s CDME%d/%s", c.str(2), c.int(0), c.str(1))
	}).in(categoryApproachClearance)
	p.command("cleared [for] [the] {visual_runway} [approach] cross {miles} dme {crossing_altitude}", func(c captured) string {
		return fmt.Sprintf("CVA%s CDME%d/%s", c.str(0), c.int(1), c.str(2))
	}).in(categoryApproachClearance)
}

// Markers that a rule leaves in the command list for a later instruction
// or an earlier one to resolve; see compose.go.
const (
	expediteMarker = "EXPEDITE"
	untilFixMarker = "UNTILFIX/"
	approvedMarker = "APPROVED"
)

func (p *phraseology) defineRadioRules() {
	p.remark(kindInformational, "radar contact")
	// The altimeter setting of an airport: "Kennedy altimeter three zero
	// one four".
	p.remark(kindInformational, "[{facility_word}] altimeter {altimeter}")
	// "Contact one five miles south of Kennedy": a position report, so
	// "radar" was lost.
	p.remark(kindInformational, "contact {miles} miles [{compass}]")
	// The ATIS letter and the altimeter setting, with the words between
	// them lost.
	p.remark(kindInformational, "{letter} {altimeter_setting}")

	tower := func(i int) func(c captured) string {
		return func(c captured) string {
			if c.has(i) && c.values[i].(av.Frequency) != 0 {
				return fmt.Sprintf("TO/%d", int(c.values[i].(av.Frequency)))
			}
			return "TO"
		}
	}
	p.command("contact tower [{frequency}]", tower(0)).in(categoryContact)
	p.command("contact {any_word} tower [{frequency}]", tower(1)).in(categoryContact)
	// The facility's name, with "contact" lost.
	p.command("{unknown_word} tower [{frequency}]", tower(1)).in(categoryContact)
	p.command("tower {frequency}", tower(0)).in(categoryContact)
	p.command("tower {unknown_word}", func(captured) string { return "TO" }).in(categoryContact)

	handoff := func(captured) string { return "FC" }
	p.command("contact approach|departure|center", handoff).in(categoryContact)
	p.command("contact {facility_frequency}", handoff).in(categoryContact)
	p.command("contact {facility_word}", handoff).in(categoryContact)
	p.remark(kindCommand, "frequency change approved")

	p.command("squawk [code] {squawk}", func(c captured) string { return "SQ" + c.str(0) })
	p.command("ident|identified", func(captured) string { return "ID" })
	p.command("squawk standby", func(captured) string { return "SQS" })
	p.command("squawk altitude|mode", func(captured) string { return "SQA" })
	p.command("transponder on", func(captured) string { return "SQON" })
	p.command("squawk normal", func(captured) string { return "SQON" })
	p.remark(kindCommand, "squawk vfr|victor")

	p.command("go ahead", func(captured) string { return "GA" })
	p.command("say request", func(captured) string { return "GA" })
	p.command("approved", func(captured) string { return approvedMarker })
	p.command("radar services terminated", func(captured) string { return "RST" })
	p.remark(kindAcknowledgment, "unable")
}

func (p *phraseology) defineAdvisoryRules() {
	p.command("traffic [at|to] [your] {traffic}", func(c captured) string { return c.str(0) }).in(categoryTraffic)
	p.command("traffic landing [the] parallel [runway] [at] [your] {traffic}", func(c captured) string { return c.str(0) }).in(categoryTraffic)
	p.command("traffic {traffic_in_sight}", func(captured) string { return "TRAFFIC/INSIGHT" }).in(categoryTraffic)
	p.remark(kindCommand, "traffic [at|to] [your] [{clock} [o'clock]] no factor")
	p.command("[do] have [the] traffic [in sight]", func(captured) string { return "TRAFFIC" }).in(categoryTraffic)
	p.command("report [the] traffic in sight", func(captured) string { return "TRAFFIC" }).in(categoryTraffic)
	p.command("report [the] traffic [and] [the] field|airport in sight", func(captured) string { return "TRAFFIC AP" }).in(categoryTraffic)
	p.command("maintain visual separation [from] [with] [the|that] [traffic]", func(captured) string { return "VISSEP" })
	p.command("caution wake turbulence", func(captured) string { return "CWT" })
	// The traffic to follow, after a wake turbulence advisory.
	p.remark(kindInformational, "follow|following [a|the] {aircraft_type}")

	atis := func(c captured) string { return "ATIS/" + c.str(0) }
	p.command("information {letter} [is] [current]", atis)
	p.command("advise [you] have information {letter}", atis)
	p.command("{letter} [is] [now] current", atis)

	// Where the airport is, and whether the pilot sees it.
	position := func(c captured) string { return fmt.Sprintf("AP/%d/%d", c.int(0), c.int(1)) }
	p.command("[the] airport|field [is|its] [gonna] [will] [be] [at] [your] {clock} o'clock {miles} [miles|mile] [report] [the] [field|airport] [in] [sight]", position).in(categoryLandmark)
	p.command("report [the] [field|airport] in sight [at] [your] {clock} o'clock {miles} [miles|mile]", position).in(categoryLandmark)
	p.command("{airport} [is] [at] [your] {clock} o'clock {miles} [miles|mile] [report] [the] [field|airport] [in] [sight]",
		func(c captured) string { return fmt.Sprintf("AP/%d/%d", c.int(1), c.int(2)) }).in(categoryLandmark)
	inSight := func(captured) string { return "AP" }
	p.command("[do] have [the] field|airport [in sight]", inSight).in(categoryLandmark)
	p.command("report [the] field|airport in sight", inSight).in(categoryLandmark)
	p.command("[do] have {airport} [in sight]", inSight).in(categoryLandmark)
	p.command("report {airport} in sight", inSight).in(categoryLandmark)

	// The landmarks of a charted visual approach.
	p.command("[the] {reporting_point} [is|its] [at] [your] {clock} o'clock {miles} [miles|mile] [report] [it] [in] [sight]",
		func(c captured) string { return fmt.Sprintf("RP/%d/%d/%s", c.int(1), c.int(2), c.str(0)) }).in(categoryLandmark)
	p.command("report [the] {reporting_point} in sight", func(c captured) string { return "RP/" + c.str(0) }).in(categoryLandmark)
	p.command("[do] have [the] {reporting_point} [in sight]", func(c captured) string { return "RP/" + c.str(0) }).in(categoryLandmark)
}

// positionIDWithin is how many words after the callsign the controller's
// own position can begin.
const positionIDWithin = 4

func (p *phraseology) defineDiscourseRules() {
	p.remark(kindThen, "then|than")
	// "At four thousand, reduce speed": the next instruction follows the
	// one in progress.
	p.remark(kindThen, "at {thousands}")
	p.remark(kindDisregard, "disregard")
	p.remark(kindCorrection, "correction")

	p.remark(kindAcknowledgment, "roger|wilco|copy|affirm|affirmative")
	p.remark(kindAcknowledgment, "hello|hey|hi|howdy")
	p.remark(kindSignOff, "have [a] good|nice|great day|night|evening|one|1")
	p.remark(kindSignOff, "good day|night|evening|morning|afternoon")
	// The controller's own position: "New York departure".
	p.remark(kindPositionID, "[{facility_word}] [{facility_word}] departure|approach|center").production.build = func(c captured) any {
		// Controllers name themselves when they first answer an aircraft.
		// Later in a transmission the same words are part of something
		// else.
		if c.start-c.d.start > positionIDWithin {
			return nil
		}
		return ""
	}
}
