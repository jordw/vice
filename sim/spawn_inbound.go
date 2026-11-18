// sim/spawn_inbound.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	gomath "math"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// createScheduledArrival creates the arrival a schedule entry describes, for
// both scenario and published entries. Vice resolves the STAR, initial
// controller, altitude, and spawn geometry from the scenario; the flow and
// arrival index were resolved when the entry was generated. All resource
// allocation--squawk, flight plan, list index--happens here; the flight
// counts as launched from its flow once launchInbound enters it in the sim.
func (s *Sim) createScheduledArrival(e ScheduledArrival) (*Aircraft, error) {
	inboundFlow, ok := s.State.InboundFlows[e.Group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", e.Group)
	}
	if e.Index < 0 || e.Index >= len(inboundFlow.Arrivals) {
		return nil, fmt.Errorf("%s: no arrival route in %s", e.Callsign, e.Group)
	}
	arr := &inboundFlow.Arrivals[e.Index]

	// The flight plan keeps the real origin even when another airport's route
	// is being flown.
	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "arrival")
	if err != nil {
		return nil, err
	}

	if err := ac.InitializeArrival(s.State.Airports[e.ArrivalAirport], arr, e.Cruise, s.PilotDelays,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.Rand, s.lg); err != nil {
		return nil, err
	}
	var filedRoute string
	if e.Source != TrafficSourceScenario {
		// The flight files the route the pair is really flown on; within the
		// facility it still flies the scenario's arrival geometry.
		filedRoute = e.FiledRoute
		s.log("%s: arrival %s->%s via %s %s (%s)", ac.ADSBCallsign, e.DepartureAirport, e.ArrivalAirport,
			e.Group, util.Select(arr.STAR == "", arr.FlightStripDisplayRoute, arr.STAR), e.How)
	}

	if err := s.finalizeArrival(ac, arr, filedRoute, e.Group); err != nil {
		return nil, err
	}
	s.joinHoldingStack(ac, e.Group, e.Index)
	return ac, nil
}

// finalizeArrival builds the arrival's NAS flight plan with controller
// assignments and registers it with STARS. filedRoute, if set, is the route
// the flight files in place of the one the arrival displays.
func (s *Sim) finalizeArrival(ac *Aircraft, arr *av.Arrival, filedRoute string, group string) error {
	nasFp := s.initFlightPlan(ac, av.FlightTypeArrival)
	switch {
	case filedRoute != "":
		nasFp.Route = filedRoute
	case arr.FlightStripDisplayRoute != "":
		nasFp.Route = arr.FlightStripDisplayRoute
	case arr.STAR != "":
		nasFp.Route = "/. " + arr.STAR
	}
	nasFp.EntryFix = ""
	nasFp.ExitFix = db.AirportDisplayId(ac.ArrivalAirport)
	nasFp.TrackingController = arr.InitialController
	nasFp.OwningTCW = s.tcwForPosition(arr.InitialController)
	ac.ControllerFrequency = arr.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = arr.Scratchpad
	nasFp.SecondaryScratchpad = arr.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && arr.IsRNAV

	if db.DB.IsARTCC(s.State.Facility) {
		nasFp.setInboundERAMAltitudes(arr.Waypoints, arr.AssignedAltitude, arr.ClearedAltitude,
			ac.Nav.FlightState.Altitude)
		nasFp.applyERAMEntries(arr.ERAM)
	}

	// Pseudo-ERAM coordination derives the entry fix; the STARS fix-pair
	// pipeline then reassigns the pair and assigns the owning position,
	// overriding the inbound-flow default above.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, "")
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	ac.maybeSetGoAround(s.State.LaunchConfig.GoAroundRate, s.Rand)

	// Decide at creation whether this pilot will spontaneously report field in sight and, among
	// those, whether they will also request the visual approach. VisualRequestDistance, when set,
	// gates the request to the first tick inside that distance.
	ac.WantsVisualApproach = s.Rand.Float32() < visualFieldProb
	if ac.WantsVisualApproach && s.Rand.Float32() < visualRequestProb {
		ac.VisualApproachRequestDistance = s.Rand.Float32Range(9, 16)
	}

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return err
	}
	// An arrival that enters already owned by a human gets its strip now;
	// others get theirs when a virtual controller hands them off.
	s.giveFlightStrip(&nasFp, nasFp.TrackingController)

	return s.associateAtSpawn(ac, nasFp)
}

// Published arrivals come when their data says, so each inbound flow spaces
// them into a single stream the way a center delivers one. Like a string of
// aircraft joined by springs, each gap opens up to arrivalTrailNM miles in
// trail, closing up toward minArrivalTrailNM only as far as the arrivals due
// over the next arrivalLookahead need to launch within maxArrivalHold of
// their data times. minArrivalTrailNM is a hard floor: when the data has more
// arrivals than fit at that spacing, they launch more than maxArrivalHold
// late. Nothing outside the flow has a say in when its arrivals launch.
// Scenario arrivals are spaced by their flow's rate instead.
const (
	minArrivalTrailNM = 6
	arrivalTrailNM    = 10
	maxArrivalHold    = 3 * time.Minute
	arrivalLookahead  = 30 * time.Minute
)

// ArrivalLaunch is the last arrival launched from an inbound flow. Its true
// airspeed at the spawn point says how long it takes to open up the trail
// behind it.
type ArrivalLaunch struct {
	Time Time
	TAS  float32
}

func (l ArrivalLaunch) timeToFly(nm float32) time.Duration {
	return time.Duration(nm / l.TAS * float32(time.Hour))
}

// arrivalFlowSpaced reports whether the inbound flow may launch its next
// arrival. The flow's queued arrivals follow its last launch at an even
// spacing: the widest, up to arrivalTrailNM, that launches the kth of them no
// later than maxArrivalHold past its data time. minArrivalTrailNM is a hard
// floor: when even that can't keep them within maxArrivalHold, they go at
// minArrivalTrailNM and launch later.
func (s *Sim) arrivalFlowSpaced(group string) bool {
	last, ok := s.ArrivalLaunches[group]
	if !ok {
		return true
	}
	elapsed := s.State.SimTime.Sub(last.Time)
	minTrail := last.timeToFly(minArrivalTrailNM)
	if elapsed < minTrail {
		return false
	}
	lc := &s.State.LaunchConfig
	trail := last.timeToFly(arrivalTrailNM)
	horizon := s.State.SimTime.Add(arrivalLookahead)
	k := 0
	for _, e := range s.Schedule.Arrivals {
		if e.SpawnTime.After(horizon) {
			break
		}
		if e.Group == group && e.DropReason == "" && lc.landsArrivals(group, e.ArrivalAirport) {
			k++
			trail = min(trail, e.SpawnTime.Add(maxArrivalHold).Sub(last.Time)/time.Duration(k))
		}
	}
	return elapsed >= trail
}

// ReadyInbound is an arrival or overflight that has been created, resources
// and all, and waits at its spawn point to enter the sim until it is
// separated from its flow's last launch. The flow creates nothing else from
// its schedule in the meantime.
type ReadyInbound struct {
	Aircraft *Aircraft
	Since    Time // when it was created
}

// readyInbound has ac wait at its spawn point as its inbound flow's next
// launch.
func (s *Sim) readyInbound(group string, ac *Aircraft) {
	if s.ReadyInbounds == nil {
		s.ReadyInbounds = make(map[string]ReadyInbound)
	}
	s.ReadyInbounds[group] = ReadyInbound{Aircraft: ac, Since: s.State.SimTime}
}

// discardReadyInbounds deletes the waiting aircraft that discard picks out,
// freeing the flight plan, squawk, and strip each one holds.
func (s *Sim) discardReadyInbounds(reason DeleteReason, discard func(group string, ac *Aircraft) bool) {
	// In order, since deleting returns list indices and strip CIDs to the
	// lists they are handed out from.
	for _, group := range util.SortedMapKeys(s.ReadyInbounds) {
		if ac := s.ReadyInbounds[group].Aircraft; discard(group, ac) {
			delete(s.ReadyInbounds, group)
			s.deleteAircraft(ac, reason)
		}
	}
}

// launchReadyInbounds launches each inbound flow's ready aircraft once it is
// separated from the flow's last launch. However long one waits pushes the
// flow's later scenario arrivals and overflights back by as much, so that
// the spacing their rates give them counts from when it launched. Published
// arrivals need no push: their miles in trail already count from the last
// arrival's launch.
func (s *Sim) launchReadyInbounds() {
	for _, group := range util.SortedMapKeys(s.ReadyInbounds) {
		r := s.ReadyInbounds[group]
		if !s.inboundSeparated(group, r.Aircraft) {
			continue
		}
		delete(s.ReadyInbounds, group)
		s.launchInbound(group, r.Aircraft)
		if d := s.State.SimTime.Sub(r.Since); d > 0 {
			s.delayInboundFlow(group, d)
		}
	}
}

// inboundSeparated reports whether ac, waiting at its spawn point, is far
// enough behind its flow's last launch: the wake turbulence separation their
// categories need, but no less than minimum radar separation.
func (s *Sim) inboundSeparated(group string, ac *Aircraft) bool {
	lead, ok := s.Aircraft[s.InboundLaunches[group]]
	if !ok {
		return true // the flow has launched nothing, or it has left the sim
	}
	// 7110.65 5-5-4: 5 miles en route and 3 in a terminal facility.
	radar := util.Select(db.DB.IsARTCC(s.State.Facility), float32(5), float32(3))
	sep := max(av.CWTDirectlyBehindSeparation(lead.CWT(), ac.CWT()), radar)
	return math.NMDistance2LL(lead.Position(), ac.Position()) >= sep
}

// launchInbound enters ac in the sim as its inbound flow's last launch.
func (s *Sim) launchInbound(group string, ac *Aircraft) {
	s.addAircraft(*ac)
	if s.InboundLaunches == nil {
		s.InboundLaunches = make(map[string]av.ADSBCallsign)
	}
	s.InboundLaunches[group] = ac.ADSBCallsign
	if ac.IsArrival() {
		if s.ArrivalLaunches == nil {
			s.ArrivalLaunches = make(map[string]ArrivalLaunch)
		}
		s.ArrivalLaunches[group] = ArrivalLaunch{Time: s.State.SimTime, TAS: ac.TAS(s.temperatureAt(ac))}
	}
}

// delayInboundFlow pushes the inbound flow's queued scenario arrivals and
// overflights later by d.
func (s *Sim) delayInboundFlow(group string, d time.Duration) {
	horizon := s.Schedule.ScenarioGeneratedUntil
	s.Schedule.Arrivals = delayScheduled(s.Schedule.Arrivals,
		func(e *ScheduledArrival) *ScheduledFlight { return &e.ScheduledFlight },
		func(e *ScheduledArrival) bool { return e.Group == group }, d, horizon)
	s.Schedule.sortArrivals()
	s.Schedule.Overflights = delayScheduled(s.Schedule.Overflights,
		func(e *ScheduledOverflight) *ScheduledFlight { return &e.ScheduledFlight },
		func(e *ScheduledOverflight) bool { return e.Group == group }, d, horizon)
	s.Schedule.sortOverflights()
}

// createScheduledOverflight creates the overflight a schedule entry describes;
// the overflight route was sampled when the entry was generated.
func (s *Sim) createScheduledOverflight(e ScheduledOverflight) (*Aircraft, error) {
	flow, ok := s.State.InboundFlows[e.Group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", e.Group)
	}
	if e.Index < 0 || e.Index >= len(flow.Overflights) {
		return nil, fmt.Errorf("%s: no overflight route in %s", e.Callsign, e.Group)
	}
	of := &flow.Overflights[e.Index]

	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "overflight")
	if err != nil {
		return nil, err
	}

	if err := ac.InitializeOverflight(of, s.PilotDelays, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.Rand, s.lg); err != nil {
		return nil, err
	}

	return ac, s.finalizeOverflight(ac, of, e.Group)
}

// finalizeOverflight builds the overflight's NAS flight plan with
// controller assignments and registers it with STARS.
func (s *Sim) finalizeOverflight(ac *Aircraft, of *av.Overflight, group string) error {
	nasFp := s.initFlightPlan(ac, av.FlightTypeOverflight)
	nasFp.Route = of.Waypoints.RouteString()
	nasFp.EntryFix = "" // TODO
	nasFp.ExitFix = ""  // TODO
	nasFp.TrackingController = of.InitialController
	nasFp.OwningTCW = s.tcwForPosition(of.InitialController)
	ac.ControllerFrequency = of.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = of.Scratchpad
	nasFp.SecondaryScratchpad = of.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && of.IsRNAV
	nasFp.TypeOfFlight = of.TypeOfFlight
	if db.DB.IsARTCC(s.State.Facility) {
		nasFp.setInboundERAMAltitudes(of.Waypoints, of.AssignedAltitude, 0, ac.Nav.FlightState.Altitude)
		nasFp.applyERAMEntries(of.ERAM)
	}

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// inbound-flow default above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, "")
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return err
	}

	// An overflight that enters already owned by a human gets its strip now;
	// others get theirs when a virtual controller hands them off.
	s.giveFlightStrip(&nasFp, nasFp.TrackingController)

	return s.associateAtSpawn(ac, nasFp)
}

// setInboundERAMAltitudes enters the hard altitude an inbound flight arrives
// with at an ERAM facility: the altitude the previous controller cleared it
// to. Conflict alert treats a level flight as free to go anywhere between its
// altitude and this one, so it has to match the clearance. In order of
// precedence, that is the assigned altitude, the "except maintain" altitude
// of a descend via, or the bottom of the route's restrictions; a flight with
// none of these holds its spawn altitude.
func (fp *FlightPlan) setInboundERAMAltitudes(wps av.WaypointArray, assigned, cleared, spawnAlt float32) {
	lowest, ok := findLowestWaypointAltitude(wps, spawnAlt)
	if ok {
		fp.PerceivedAssigned = lowest
	}
	switch {
	case assigned > 0:
		fp.AssignedAltitude = int(assigned)
	case cleared > 0:
		fp.AssignedAltitude = int(cleared)
	case ok:
		fp.AssignedAltitude = lowest
	default:
		fp.AssignedAltitude = int(spawnAlt)
	}
}

// findLowestWaypointAltitude finds the lowest altitude restriction target from
// the waypoints: the bottom of the procedure they make up, which is the
// altitude an ERAM data block shows for a flight descending it. Returns the
// altitude and true if found, or 0 and false if no restrictions exist.
func findLowestWaypointAltitude(wps av.WaypointArray, initialAlt float32) (int, bool) {
	lowestAlt := gomath.MaxInt
	for _, wp := range wps {
		if wp.AltitudeRestriction() == nil {
			continue
		}
		if target := int(wp.AltitudeRestriction().TargetAltitude(initialAlt)); target < lowestAlt {
			lowestAlt = target
		}
	}
	if lowestAlt == gomath.MaxInt {
		return 0, false
	}
	return lowestAlt, true
}

// associateAtSpawn registers nasFp with the STARS computer and, if the
// tracking controller is virtual, immediately associates it with ac so it
// never appears in UnassociatedFlightPlans / the STARS FLIGHT PLAN list.
// External-facility-owned flight plans stay unassociated until the handoff
// into the facility completes.
func (s *Sim) associateAtSpawn(ac *Aircraft, nasFp FlightPlan) error {
	created, err := s.STARSComputer.CreateFlightPlan(nasFp)
	if err != nil {
		return err
	}
	if !s.isVirtualController(created.TrackingController) {
		return nil
	}
	fp := s.STARSComputer.takeFlightPlanByACID(created.ACID)
	if fp == nil {
		return nil
	}
	if s.State.IsLocalController(fp.TrackingController) {
		fp.LastLocalController = fp.TrackingController
	}
	ac.AssociateFlightPlan(fp)
	s.eventStream.Post(Event{
		Type: FlightPlanAssociatedEvent,
		ACID: fp.ACID,
	})
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Holding

// maxHeldArrivals is how many arrivals a holding stack takes; arrivals that
// would join a full one wait to spawn.
const maxHeldArrivals = 5

// holdingAreaNM is how far from its fix a hold takes an aircraft: a 1.5
// minute leg at 265 knots and the turn at its end.
const holdingAreaNM = 9

// HoldingStack is a virtual controller's stack of arrivals holding at a
// fix, 1,000' apart. Each arrival joins at the top, and those already
// there step down to make room for it; when the holding ends, they leave
// from the bottom up.
type HoldingStack struct {
	Hold     av.Hold
	Location math.Point2LL
	// Top is the altitude the newest arrival holds at: no higher than any
	// of the arrivals would hold at on its own, so none climbs to join.
	Top float32
	// Floor is the lowest altitude the hold may be flown at: its charted
	// minimum and 1,000' above the terrain.
	Floor float32
	// Aircraft are the arrivals in the stack in the order they joined, so
	// the first is at the bottom.
	Aircraft []HeldArrival
	// Left is the last arrival to leave; the next one is cleared out of
	// the hold once it is clear of the fix.
	Left av.ADSBCallsign
}

// HeldArrival is an arrival in a holding stack.
type HeldArrival struct {
	Callsign av.ADSBCallsign
	Altitude float32
	Released bool // it has been cleared out of the hold
}

// hasRoom reports whether the stack can take another arrival with its top
// at top.
func (hs *HoldingStack) hasRoom(top float32) bool {
	n := len(hs.Aircraft)
	return n < maxHeldArrivals && top-1000*float32(n) >= hs.Floor
}

// findArrivalHoldFixes finds the fix where each of the scenario's arrivals
// can be held.
func (s *Sim) findArrivalHoldFixes() map[string]map[int]string {
	fixes := make(map[string]map[int]string)
	for name, flow := range s.State.InboundFlows {
		for i := range flow.Arrivals {
			arr := &flow.Arrivals[i]
			if j := s.arrivalHoldFix(arr); j != -1 {
				if fixes[name] == nil {
					fixes[name] = make(map[int]string)
				}
				fixes[name][i] = arr.Waypoints[j].Fix
			}
		}
	}
	return fixes
}

// arrivalHoldFix returns the index of the waypoint where a virtual
// controller can hold arrivals flying arr: the first fix past the spawn
// point, if a virtual controller still has the track there. It returns -1
// if a human controller has it by then or the route leaves for a heading
// before reaching a fix.
func (s *Sim) arrivalHoldFix(arr *av.Arrival) int {
	if !s.isVirtualController(arr.InitialController) {
		return -1
	}
	toHuman := func(g av.WaypointActionGroup) bool {
		return g.Actions.HumanHandoff ||
			(g.Actions.HandoffController != "" && !s.isVirtualController(g.Actions.HandoffController))
	}
	for i, wp := range arr.Waypoints {
		if _, ok := db.DB.LookupWaypoint(wp.Fix); ok && i > 0 {
			return i
		}
		// The handoffs at a fix are made as the aircraft passes it, so one
		// at the holding fix waits until the aircraft leaves the hold.
		if slices.ContainsFunc(wp.ActionGroups(), toHuman) || wp.AssignsHeading() {
			return -1
		}
	}
	return -1
}

// arrivalHold returns the hold that arrivals flying arr fly at its ith
// waypoint when the stack's top is at alt: one charted there, preferably
// for alt, or else a standard hold on the course into the fix.
func (s *Sim) arrivalHold(arr *av.Arrival, i int, alt float32) av.Hold {
	fix := arr.Waypoints[i].Fix
	holds := db.DB.EnrouteHolds[fix]
	if j := slices.IndexFunc(holds, func(h av.Hold) bool {
		return alt >= float32(h.MinimumAltitude) && (h.MaximumAltitude == 0 || alt <= float32(h.MaximumAltitude))
	}); j != -1 {
		return holds[j]
	} else if len(holds) > 0 {
		return holds[0]
	}

	course := math.Heading2LL(arr.Waypoints[i-1].Location, arr.Waypoints[i].Location, s.State.NmPerLongitude)
	return av.Hold{
		Fix:           fix,
		InboundCourse: math.TrueToMagnetic(course, s.State.MagneticVariation),
		TurnDirection: av.TurnRight,
	}
}

// holdingStackFull reports whether the arrival would join a holding stack
// that has no room for it.
func (s *Sim) holdingStackFull(group string, index int) bool {
	fix, ok := s.State.ArrivalHoldFixes[group][index]
	if !ok {
		return false
	}
	hs, ok := s.HoldingStacks[fix]
	return ok && !hs.hasRoom(hs.Top)
}

// joinHoldingStack has a newly created arrival hold at its holding fix if
// center is holding arrivals there or still has some there in its stack.
// It joins the stack at the top, holding at the altitude it spawned at or
// lower if it is to cross the fix lower, and the arrivals already there
// step down to make room. One that there is no room for flies on.
func (s *Sim) joinHoldingStack(ac *Aircraft, group string, index int) {
	arr := &s.State.InboundFlows[group].Arrivals[index]
	i := s.arrivalHoldFix(arr)
	if i == -1 {
		return
	}
	fix := arr.Waypoints[i].Fix
	hs, ok := s.HoldingStacks[fix]
	if !ok && !s.State.LaunchConfig.ArrivalHolds[fix] {
		return
	}

	alt := ac.Altitude()
	if ar := arr.Waypoints[i].AltitudeRestriction(); ar != nil {
		alt = min(alt, ar.Range[1])
	}
	if !ok {
		hold := s.arrivalHold(arr, i, alt)
		terrain := s.terrain.HighestTerrainIn(math.BoundLatLongCircle(arr.Waypoints[i].Location, holdingAreaNM))
		hs = &HoldingStack{
			Hold:     hold,
			Location: arr.Waypoints[i].Location,
			Top:      alt,
			Floor:    float32(max(hold.MinimumAltitude, terrain+1000)),
		}
	}
	top := min(hs.Top, alt)
	if !hs.hasRoom(top) {
		return
	}

	hs.Top = top
	n := len(hs.Aircraft)
	for j := range hs.Aircraft {
		s.lowerHeldArrival(&hs.Aircraft[j], top-1000*float32(n-j))
	}
	ac.Nav.HoldAtFix(string(ac.ADSBCallsign), fix, &hs.Hold)
	s.assignHoldingAltitude(ac, top)
	hs.Aircraft = append(hs.Aircraft, HeldArrival{Callsign: ac.ADSBCallsign, Altitude: top})

	if s.HoldingStacks == nil {
		s.HoldingStacks = make(map[string]*HoldingStack)
	}
	s.HoldingStacks[fix] = hs
}

// lowerHeldArrival has the held arrival descend to alt if it is holding
// higher; held arrivals never climb.
func (s *Sim) lowerHeldArrival(h *HeldArrival, alt float32) {
	if alt >= h.Altitude {
		return
	}
	h.Altitude = alt
	if ac := s.inboundAircraft(h.Callsign); ac != nil {
		s.assignHoldingAltitude(ac, alt)
	}
}

func (s *Sim) assignHoldingAltitude(ac *Aircraft, alt float32) {
	defer ac.commandsFrom(string(ac.ControllerFrequency), false)()
	ac.Nav.AssignAltitudeNow(alt, false)
	s.recordVirtualAltitudeEntry(ac.FlightPlan, ac.ControllerFrequency, int(alt), false)
}

// inboundAircraft returns the aircraft with the callsign, whether it is in
// the sim or waiting at its spawn point to enter it.
func (s *Sim) inboundAircraft(callsign av.ADSBCallsign) *Aircraft {
	if ac, ok := s.Aircraft[callsign]; ok {
		return ac
	}
	for _, r := range s.ReadyInbounds {
		if r.Aircraft.ADSBCallsign == callsign {
			return r.Aircraft
		}
	}
	return nil
}

// updateHoldingStacks drops the arrivals that have left their holding
// stacks and the stacks left empty. Where center has stopped holding, the
// arrivals are cleared out of the hold one at a time from the bottom.
func (s *Sim) updateHoldingStacks() {
	for _, fix := range util.SortedMapKeys(s.HoldingStacks) {
		hs := s.HoldingStacks[fix]
		hs.Aircraft = slices.DeleteFunc(hs.Aircraft, func(h HeldArrival) bool {
			// One waiting to enter the sim is still to come.
			ac, ok := s.Aircraft[h.Callsign]
			left := ok && !ac.Nav.HoldingAt(fix)
			if left {
				hs.Left = h.Callsign
			}
			return left
		})

		if len(hs.Aircraft) == 0 {
			delete(s.HoldingStacks, fix)
		} else if !s.State.LaunchConfig.ArrivalHolds[fix] {
			s.releaseHeldArrival(fix, hs)
		}
	}
}

// releaseHeldArrival clears the arrival at the bottom of the stack out of
// the hold once the last one to leave is minArrivalTrailNM from the fix.
func (s *Sim) releaseHeldArrival(fix string, hs *HoldingStack) {
	h := &hs.Aircraft[0]
	ac, ok := s.Aircraft[h.Callsign]
	if !ok || h.Released {
		return
	}
	if prev, ok := s.Aircraft[hs.Left]; ok && math.NMDistance2LL(prev.Position(), hs.Location) < minArrivalTrailNM {
		return
	}

	defer ac.commandsFrom(string(ac.ControllerFrequency), false)()
	ac.Nav.LeaveHold(fix)
	h.Released = true
}
