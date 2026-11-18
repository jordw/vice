// nav/restriction.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"cmp"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// The route's crossing restrictions: which of the published and
// controller-issued altitude and speed restrictions apply to the aircraft,
// the altitude and speed they call for, how they carry past a fix, and the
// via clearances that put a procedure's restrictions in force.

func (nav *Nav) controllerAltitudeRestriction(wp *av.Waypoint) *av.AltitudeRestriction {
	if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
		return nfa.Arrive.Altitude
	}
	if wp.SyntheticCrossing() {
		return wp.AltitudeRestriction()
	}
	return nil
}

// chartedAltitudeRestriction returns wp's published altitude restriction,
// or nil if it doesn't apply to the aircraft. An approach's restrictions
// apply only once the aircraft is cleared for it, and an arrival climbs
// only when a controller tells it to, so one above it doesn't apply.
func (nav *Nav) chartedAltitudeRestriction(wp *av.Waypoint) *av.AltitudeRestriction {
	if wp.OnApproach() && !nav.Approach.Cleared {
		return nil
	}
	ar := wp.AltitudeRestriction()
	if ar != nil && wp.OnSTAR() && !wp.SyntheticCrossing() && ar.Range[0] > nav.FlightState.Altitude {
		return nil
	}
	return ar
}

// chartedSpeedRestriction returns wp's inline speed restriction. Published
// approach restrictions require approach clearance; synthetic controller
// crossings apply regardless of clearance.
func (nav *Nav) chartedSpeedRestriction(wp *av.Waypoint) *av.SpeedRestriction {
	if wp.OnApproach() && !nav.Approach.Cleared && !wp.SyntheticCrossing() {
		return nil
	}
	return wp.SpeedRestriction()
}

// carryRestrictions keeps wp's restrictions in force past it: an aircraft
// that couldn't meet the altitude keeps trying, and a published speed is held
// until something replaces it. The controller's crossing altitude for the
// fix, if given, replaces the charted one after the fix as well as before.
func (nav *Nav) carryRestrictions(wp av.Waypoint) {
	if ar := cmp.Or(nav.controllerAltitudeRestriction(&wp), nav.chartedAltitudeRestriction(&wp)); ar != nil &&
		(!nav.Approach.Cleared || ar.Range[0] < nav.FlightState.Altitude) {
		// Don't climb if we're cleared approach and below the next
		// fix's altitude. Copy the value since the pointer into the
		// slice element could become stale if the slice is reallocated.
		arCopy := *ar
		// Only the side of the restriction the aircraft missed carries
		// past the fix. One it met doesn't apply again, so an aircraft
		// that later leaves the altitude for restrictions ahead isn't
		// called back to it.
		if nav.FlightState.Altitude >= arCopy.Range[0] {
			arCopy.Range[0] = 0
		}
		if nav.FlightState.Altitude <= arCopy.Range[1] {
			arCopy.Range[1] = av.MaxAltitude
		}
		nav.Altitude.Restriction = &arCopy
	}
	if sr := nav.chartedSpeedRestriction(&wp); sr != nil && !wp.OnSID() {
		// Carry on the speed restriction unless it's a SID
		srCopy := *sr
		nav.Speed.Restriction = &srCopy
	}
}

// descentPlanningFraction is the share of its maximum descent rate that an
// aircraft plans the legs between restrictions at. The aircraft eases into
// the altitude it plans for each fix and starts down again after it, and it
// levels at 10,000 feet to slow to 250 knots; the rest of the rate makes up
// for those.
const descentPlanningFraction = 0.75

// altitudeTarget is the altitude restriction ahead that the aircraft flies
// toward.
type altitudeTarget struct {
	altitude float32
	fix      string
	// level is set when the restrictions ahead require exactly the current
	// altitude at fix, so the aircraft stays level until it gets there.
	level bool
}

// findAltitudeTarget returns the altitude restriction ahead that the
// aircraft flies toward, if there is one.
func (nav *Nav) findAltitudeTarget() (altitudeTarget, bool) {
	if nav.Approach.Cleared {
		t, ok := nav.approachAltitudeTarget()
		return t.altitudeTarget, ok
	}
	return nav.routeAltitudeTarget()
}

// waypointsToHold returns the route up to the fix of an upcoming hold:
// restrictions after it don't apply yet.
func (nav *Nav) waypointsToHold() []av.Waypoint {
	wps := nav.Waypoints
	for i := range wps {
		if hold := nav.Heading.Hold; hold != nil && hold.Hold.Fix == wps[i].Fix {
			return wps[:i+1]
		}
		if nfa, ok := nav.FixAssignments[wps[i].Fix]; ok && nfa.Hold != nil {
			return wps[:i+1]
		}
	}
	return wps
}

// routeAltitudeTarget scans waypoints to determine the target altitude and
// fix, using the reverse-walk logic to satisfy all downstream altitude
// constraints. Returns the target and true if found.
func (nav *Nav) routeAltitudeTarget() (altitudeTarget, bool) {
	// On a heading the route's fixes don't apply.
	if nav.Heading.Assigned != nil {
		return altitudeTarget{}, false
	}

	wps := nav.waypointsToHold()
	if nav.Prespawn {
		// Simplified forward scan for prespawn: find the first waypoint
		// with an actionable restriction. Skips FixAssignment lookups
		// and the reverse walk with ClampRange for intermediate waypoints.
		for i := range wps {
			ar := nav.chartedAltitudeRestriction(&wps[i])
			if ar == nil {
				continue
			}
			if ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
				if _, ok := ar.ExactValue(); ok {
					return altitudeTarget{altitude: nav.FlightState.Altitude, fix: wps[i].Fix, level: true}, true
				}
				continue
			}
			alt := util.Select(ar.Range[1] != av.MaxAltitude, ar.Range[1], nav.FinalAltitude)
			return altitudeTarget{altitude: alt, fix: wps[i].Fix}, true
		}
		return altitudeTarget{}, false
	}

	getRestriction := func(i int) *av.AltitudeRestriction { return nav.routeAltitudeRestriction(wps, i) }

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft. Along the way, note the nearest met
	// restriction that requires exactly the current altitude.
	lastWp, levelWp := -1, -1
	for i := range slices.Backward(wps) {
		r := getRestriction(i)
		if r == nil {
			continue
		}
		// Skip restrictions that don't apply (e.g. "at or above" if we're
		// already above.) I think(?) we would actually bail out and return
		// nil if we find one that doesn't apply, under the principle that
		// we should also already be meeting any restrictions that are
		// before it, but this seems less risky.
		if r.TargetAltitude(nav.FlightState.Altitude) != nav.FlightState.Altitude {
			lastWp = i
			break
		}
		if _, ok := r.ExactValue(); ok {
			levelWp = i
		}
	}
	if lastWp == -1 {
		if levelWp != -1 {
			// Everything ahead is met, but the aircraft still has to cross
			// this fix at its current altitude rather than leave for its
			// cleared altitude.
			return altitudeTarget{altitude: nav.FlightState.Altitude, fix: wps[levelWp].Fix, level: true}, true
		}
		// No applicable altitude restrictions found, so nothing to do here.
		return altitudeTarget{}, false
	}

	last := getRestriction(lastWp)
	descending := nav.FlightState.Altitude > last.TargetAltitude(nav.FlightState.Altitude)

	// The reverse walk plans a descent with some of the aircraft's descent
	// rate in reserve; climbs are planned at the full rate.
	altRate := util.Select(descending, descentPlanningFraction*nav.Perf.Rate.Descent, nav.Perf.Rate.Climb)

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := last.Range

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	fix := wps[lastWp].Fix // first one with an alt restriction
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += math.NMDistance2LLFast(wps[i+1].Location, wps[i].Location,
			nav.FlightState.NmPerLongitude)

		// Does this one have a relevant altitude restriction?
		restr := getRestriction(i)
		if restr == nil {
			continue
		}

		fix = wps[i].Fix

		// Change in altitude planned for before reaching this waypoint.
		// Coincident waypoints allow no change at all; handling them here
		// also avoids 0/0 on a departure's first tick, when GS is 0.
		var dalt float32
		if sumDist > 0 {
			// TODO: account for decreasing GS with altitude?
			// TODO: incorporate a simple wind model in GS?
			eta := sumDist / nav.FlightState.GS * 3600 // seconds
			dalt = altRate * eta / 60
		}

		// possibleRange is altitude range the aircraft could have at this
		// waypoint, given its performance characteristics and assuming it
		// will meet the constraint at the subsequent waypoint with an
		// altitude restriction.
		//
		// Note that dalt only applies to one limit, since the aircraft can
		// always maintain its current altitude between waypoints; which
		// limit depends on whether it is climbing or descending (but then
		// which one and the addition/subtraction are confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := altRange
		if !descending {
			possibleRange[0] -= dalt
		} else {
			possibleRange[1] += dalt
		}

		// Limit the possible range according to the restriction at the
		// current waypoint. A leg that's too steep to fly doesn't excuse
		// the restrictions before it; ClampRange anchors altRange on this
		// restriction in that case, so keep walking back to find the
		// earliest restriction that binds.
		altRange, _ = restr.ClampRange(possibleRange)

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Prefer to be higher rather than low; deal with "at or above" here as well.
	alt := util.Select(altRange[1] != av.MaxAltitude, altRange[1], nav.FinalAltitude)

	// But leave arrivals at their current altitude if it's acceptable;
	// don't climb just because we can.
	if descending {
		ar := av.AltitudeRestriction{NavigationRestriction: av.NavigationRestriction{Range: altRange}}
		if ar.Target(nav.FlightState.Altitude) == nav.FlightState.Altitude {
			alt = nav.FlightState.Altitude
		}
	} else {
		// For climbing aircraft, if the constraint is "at" (not "at or above"),
		// target that specific altitude instead of climbing higher
		if altRange[0] != 0 && altRange[0] == altRange[1] {
			alt = altRange[0]
		}
	}

	level := altRange[0] == altRange[1] && altRange[0] == nav.FlightState.Altitude
	return altitudeTarget{altitude: alt, fix: fix, level: level}, true
}

// routeAltitudeRestriction returns the altitude restriction the aircraft
// flies at wps[i]: one the controller assigned there, or else the charted
// one, unless the controller assigned a crossing altitude at a fix after it.
func (nav *Nav) routeAltitudeRestriction(wps []av.Waypoint, i int) *av.AltitudeRestriction {
	wp := &wps[i]
	if ar := nav.controllerAltitudeRestriction(wp); ar != nil {
		return ar
	}
	ar := nav.chartedAltitudeRestriction(wp)
	if ar != nil && len(nav.FixAssignments) > 0 {
		// Explicit loop avoids slices.ContainsFunc which copies the
		// large Waypoint struct by value for each element via the closure.
		for j := i + 1; j < len(wps); j++ {
			if fa, ok := nav.FixAssignments[wps[j].Fix]; ok && fa.Arrive.Altitude != nil {
				return nil
			}
		}
	}
	return ar
}

// approachTarget is what a pilot cleared for an approach descends toward.
type approachTarget struct {
	altitudeTarget
	// minimum is the highest minimum altitude at the fixes up to fix: the
	// lowest the aircraft may level at on its way there.
	minimum float32
	// descend is set when the aircraft descends now, in a straight line to
	// fix.
	descend bool
}

// approachAltitudeTarget returns what a pilot cleared for an approach
// descends toward along its route: the final approach fix at its altitude, or, past
// it or below it, the end of the approach. The restrictions on the way are
// flown as charted: a minimum is the lowest the aircraft may be there, not an
// altitude to descend to, and a restriction above the aircraft doesn't apply.
//
// The aircraft has to get down to the target and to each maximum on the way.
// A level aircraft stays level until it is on a 3 degree path to one of them,
// as on a glideslope, and then descends in a straight line to the one that
// needs the steepest descent. One that is already descending on its own, or
// is past the final approach fix, keeps on down in that straight line. A
// descent the controller gave ends at its altitude, and the aircraft then
// waits there as a level one does. Until then, the target is the one whose
// path it will meet first. Where the line would pass below a minimum, it
// aims for that minimum instead.
//
// On a heading the approach's fixes don't apply, except that a pilot
// cleared for a charted visual descends along the procedure it is joining.
func (nav *Nav) approachAltitudeTarget() (approachTarget, bool) {
	if nav.Heading.Assigned != nil && nav.Approach.Assigned.Type != av.ChartedVisualApproach {
		return approachTarget{}, false
	}
	wps := nav.waypointsToHold()
	alt := nav.FlightState.Altitude
	type limit struct {
		altitude, dist float32
		fix            string
	}
	var floors, ceilings []limit
	dist := float32(0)
	prev := nav.FlightState.Position
	for i := range wps {
		dist += math.NMDistance2LLFast(prev, wps[i].Location, nav.FlightState.NmPerLongitude)
		prev = wps[i].Location
		ar := nav.routeAltitudeRestriction(wps, i)
		if ar == nil || ar.Range[0] > alt {
			continue
		}
		lim := limit{dist: max(dist, 0.01), fix: wps[i].Fix}
		if wps[i].FAF() {
			lim.altitude = util.Select(ar.Range[0] > 0, ar.Range[0], ar.Range[1])
			floors, ceilings = append(floors, lim), append(ceilings, lim)
			break
		}
		if ar.Range[0] > 0 {
			lim.altitude = ar.Range[0]
			floors = append(floors, lim)
		}
		if ar.Range[1] < alt {
			lim.altitude = ar.Range[1]
			ceilings = append(ceilings, lim)
		}
	}
	if len(ceilings) == 0 {
		return approachTarget{}, false
	}

	gradient := func(l limit) float32 { return (alt - l.altitude) / l.dist }
	descending := nav.Approach.PassedFAF ||
		(nav.FlightState.AltitudeRate < -50 && !nav.descendingToClearedAltitude())
	var aim *limit
	next, nextDist := ceilings[0], math.Infinity
	for i, c := range ceilings {
		// How far the aircraft flies level before it is on a 3 degree path
		// to c. A little below the path counts as on it, so that an
		// aircraft following it doesn't stop and start.
		level := c.dist - (alt-c.altitude+50)/ftPerNMOnGlideslope
		if descending || level <= 0 {
			if aim == nil || gradient(c) > gradient(*aim) {
				aim = &ceilings[i]
			}
		} else if level < nextDist {
			next, nextDist = c, level
		}
	}
	minimum := func(to limit) float32 {
		var m float32
		for _, f := range floors {
			if f.dist <= to.dist {
				m = max(m, f.altitude)
			}
		}
		return m
	}
	if aim == nil {
		return approachTarget{altitudeTarget: altitudeTarget{altitude: next.altitude, fix: next.fix},
			minimum: minimum(next)}, true
	}

	g := gradient(*aim)
	var floor *limit
	for i, f := range floors {
		if f.dist < aim.dist && alt-g*f.dist < f.altitude && (floor == nil || gradient(f) < gradient(*floor)) {
			floor = &floors[i]
		}
	}
	if floor != nil {
		aim = floor
	}
	return approachTarget{altitudeTarget: altitudeTarget{altitude: aim.altitude, fix: aim.fix, level: aim.altitude == alt},
		minimum: minimum(*aim), descend: true}, true
}

// descendingToClearedAltitude reports whether the aircraft's descent is one
// the controller gave it, kept as its cleared altitude: that descent ends
// there, rather than carrying on below the approach's 3 degree path.
func (nav *Nav) descendingToClearedAltitude() bool {
	c := nav.Altitude.Cleared
	return c != nil && !c.IsFloor && c.Altitude <= nav.FlightState.Altitude
}

// LowestProcedureAltitude returns the lowest applicable published altitude
// target on the assigned route, or false if there are no applicable restrictions.
func (nav *Nav) LowestProcedureAltitude() (float32, bool) {
	var lowest float32
	found := false
	wps := nav.AssignedWaypoints()
	for i := range wps {
		if ar := nav.chartedAltitudeRestriction(&wps[i]); ar != nil {
			alt := ar.TargetAltitude(nav.FlightState.Altitude)
			if !found || alt < lowest {
				lowest = alt
				found = true
			}
		}
	}
	return lowest, found
}

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (wp *av.Waypoint, sr *av.SpeedRestriction, ok bool) {
	if nav.Prespawn {
		return nil, nil, false
	}

	for i := range nav.Waypoints {
		wp := &nav.Waypoints[i]
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
			return wp, nfa.Arrive.Speed, true
		}
		if wsr := nav.chartedSpeedRestriction(wp); wsr != nil {
			return wp, wsr, true
		}
	}
	return nil, nil, false
}

// controllerSpeedAt reports whether the speed restriction at wp is one a
// controller gave rather than a charted one.
func (nav *Nav) controllerSpeedAt(wp *av.Waypoint) bool {
	return nav.FixAssignments[wp.Fix].Arrive.Speed != nil || wp.SyntheticCrossing()
}

// restrictedSpeed returns the speed a pilot would choose given a speed
// restriction, accounting for aircraft type. Heavy jets fly close to
// limits for fuel efficiency; lighter aircraft have more margin.
func (nav *Nav) restrictedSpeed(sr *av.SpeedRestriction, natural float32) float32 {
	ias := sr.IAS(nav.FlightState.Altitude)
	lo, hi := ias.Range[0], ias.Range[1]

	// "At" restriction: exact compliance required.
	if lo == hi {
		return lo
	}

	// Margin from the boundary depends on aircraft weight class.
	// Heavies need speed for efficiency and fly close to limits.
	margin := float32(10)
	if nav.Perf.WeightClass == "H" || nav.Perf.WeightClass == "J" {
		margin = 5
	}

	// Shrink the allowed range by the margin on each real bound.
	innerLo := lo
	innerHi := hi
	if lo > 0 { // lo==0 means no floor
		innerLo = lo + margin
	}
	if hi < av.MaxRestrictionSpeed { // sentinel means no ceiling
		innerHi = hi - margin
	}

	// If margins overlap (very narrow range), just use the midpoint.
	if innerLo > innerHi {
		return (lo + hi) / 2
	}

	return math.Clamp(natural, innerLo, innerHi)
}

// ClimbViaSID is "climb via SID", with "except maintain exceptAlt" if it is
// non-nil. Otherwise the aircraft climbs to its cruise altitude: the
// scenario's cleared altitude for a departure is an earlier controller's
// "except maintain", and cruise is the only top altitude it gives a SID.
func (nav *Nav) ClimbViaSID(exceptAlt *float32, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("ClimbViaSID", exceptAlt, simTime, delayReduction)()
	intent := speech.ProcedureIntent{Type: speech.ProcedureClimbViaSID, ExceptAltitude: exceptAlt}
	if exceptAlt != nil {
		if *exceptAlt > nav.Perf.Ceiling {
			return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
		}
		if *exceptAlt > nav.FinalAltitude {
			return speech.MakeUnableIntent("unable. We're only filed up to {alt}", nav.FinalAltitude)
		}
		if alt := nav.FlightState.Altitude; alt-*exceptAlt >= 1000 {
			intent.CurrentAltitude = &alt
		}
	}
	if !nav.ClimbViaSIDAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not flying a departure procedure")
	}

	nav.resumeProcedureCourse(simTime, delayReduction)
	return intent
}

// DescendViaSTAR is "descend via STAR", with "except maintain exceptAlt" if
// it is non-nil; otherwise the aircraft descends to the STAR's last
// restriction.
func (nav *Nav) DescendViaSTAR(exceptAlt *float32, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("DescendViaSTAR", exceptAlt, simTime, delayReduction)()
	intent := speech.ProcedureIntent{Type: speech.ProcedureDescendViaSTAR, ExceptAltitude: exceptAlt}
	if exceptAlt != nil {
		if *exceptAlt > nav.Perf.Ceiling {
			return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
		}
		alt := nav.FlightState.Altitude
		if *exceptAlt-alt >= 5000 {
			return speech.MakeUnableIntent("unable. We're already at {alt}", alt)
		}
		if *exceptAlt-alt >= 1000 {
			intent.CurrentAltitude = &alt
		}
	}
	if !nav.DescendViaSTARAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not on a STAR")
	}

	nav.resumeProcedureCourse(simTime, delayReduction)
	return intent
}

// resumeProcedureCourse puts the aircraft back on course after a via
// clearance. One flying a heading to rejoin the procedure keeps doing so;
// the clearance is a new altitude, so the heading no longer captures an
// altitude floor when it takes effect.
func (nav *Nav) resumeProcedureCourse(simTime Time, delayReduction time.Duration) {
	if nav.pendingJoin() == nil {
		nav.enqueueOnCourse(simTime, delayReduction)
	} else if dh := nav.DeferredNavHeading; dh != nil {
		dh.SnapshotAltitudeOnEffect = false
	}
}

// ClimbViaSIDAtPassedFix carries out a /cvs or /cv route action at the fix
// the aircraft just passed. The aircraft is already flying its route, so
// unlike ClimbViaSID it isn't put back on course. It returns false,
// changing nothing, if no fix ahead is on a SID. An excepted altitude below
// the aircraft is descended to, as instructed.
func (nav *Nav) ClimbViaSIDAtPassedFix(exceptAlt *float32) bool {
	defer nav.record("ClimbViaSIDAtPassedFix", exceptAlt)()
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSID) {
		return false
	}

	ceiling := nav.FinalAltitude
	if exceptAlt != nil {
		ceiling = *exceptAlt
	}
	nav.flyProcedureRestrictions(&ClearedAltitude{Altitude: ceiling})
	return true
}

// DescendViaSTARAtPassedFix is the /dvs and /dv counterpart of
// ClimbViaSIDAtPassedFix; an excepted altitude above the aircraft is climbed
// back to.
func (nav *Nav) DescendViaSTARAtPassedFix(exceptAlt *float32) bool {
	defer nav.record("DescendViaSTARAtPassedFix", exceptAlt)()
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSTAR) {
		return false
	}

	var floor *ClearedAltitude
	if exceptAlt != nil {
		floor = &ClearedAltitude{Altitude: *exceptAlt, IsFloor: true}
	}
	nav.flyProcedureRestrictions(floor)
	return true
}

// flyProcedureRestrictions cancels the assigned altitude and speed so that
// the restrictions of the procedure ahead govern, limited by cleared if it
// is non-nil. A published speed restriction the aircraft is already holding
// stays in effect: a via clearance cancels only assigned speeds (7110.65
// 5-7-1).
func (nav *Nav) flyProcedureRestrictions(cleared *ClearedAltitude) {
	nav.Altitude = Altitude{Cleared: cleared}
	nav.Speed = Speed{Restriction: nav.Speed.Restriction}
	nav.clearAfterFixSpeeds()
}
