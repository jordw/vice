// sim/spawn_inbound_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// A flight at an airport the scenario lands no traffic at must not hold up the
// arrivals behind it: the queue is shared across every airport a facility
// works, so one stuck at the head would stall all of them.
func TestZeroRateArrivalsDoNotBlock(t *testing.T) {
	day := traffic.FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC))
	spawn := NewSimTime(time.Date(2026, time.April, 15, 8, 0, 0, 0, time.UTC))
	arrival := func(airport av.ICAOAirportCode, callsign, group string) ScheduledArrival {
		return ScheduledArrival{
			ScheduledFlight: ScheduledFlight{Callsign: callsign, AircraftType: "B738",
				DepartureAirport: "KATL", ArrivalAirport: airport,
				Source: TrafficSourceHistorical, SpawnTime: spawn, Day: day, Minute: 8 * 60},
			Group: group,
		}
	}

	s := NewTestSim(testLogger())
	s.State.SimTime = spawn
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates: map[string]map[string]float32{
			"PUCKY1": {"KFRG": 0, "KJFK": 12},
			"CAMRN5": {"KJFK": 12},
		},
		InboundFlowEnabled: map[string]map[string]bool{
			"PUCKY1": {"KFRG": false, "KJFK": true},
			"CAMRN5": {"KJFK": true},
		},
	}
	s.Schedule.Arrivals = []ScheduledArrival{
		arrival("KFRG", "DAL1", "PUCKY1"), // PUCKY1 lands nothing at KFRG
		arrival("KFRG", "DAL2", "PUCKY1"),
		arrival("KJFK", "DAL3", "PUCKY1"), // but it does at KJFK
		arrival("KJFK", "DAL4", "CAMRN5"), // and this one belongs to another flow
	}

	// The KFRG arrivals are discarded when due rather than deferring, so the
	// KJFK ones behind them are reached; creating those fails in this bare
	// test sim (there are no inbound flows to fly), but only after they were
	// taken up in their turn.
	s.spawnScheduledArrivals()

	if s.discardedArrivals["KFRG"] != 2 {
		t.Errorf("discarded %v, expected both KFRG arrivals", s.discardedArrivals)
	}
	if n := len(s.Schedule.Arrivals); n != 0 {
		t.Errorf("%d arrivals left in the queue, expected all processed", n)
	}
}

// An inbound flow's arrivals open up to 10 miles in trail where the data
// leaves room, closing up toward 6 only as far as it takes to hold none of
// those coming up behind more than 3 minutes past its data time. 6 miles is a
// hard floor, however late a burst runs. At 240 knots, 6 miles takes 90
// seconds and 10 takes 150.
func TestArrivalFlowSpacing(t *testing.T) {
	launch := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	arrival := func(callsign, group string, airport av.ICAOAirportCode, after time.Duration) ScheduledArrival {
		return testScheduledArrival(callsign, group, airport, launch.Add(after))
	}

	s := NewTestSim(testLogger())
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates:   map[string]map[string]float32{"TEST": {"KJFK": 12, "KFRG": 6}},
		InboundFlowEnabled: map[string]map[string]bool{"TEST": {"KJFK": true, "KFRG": false}},
	}
	s.State.SimTime = launch
	if !s.arrivalFlowSpaced("TEST") {
		t.Error("held a flow that has launched nothing")
	}

	for _, tc := range []struct {
		name  string
		queue []ScheduledArrival
		trail time.Duration
	}{
		{"a lone pair opens to 10 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "TEST", "KJFK", 12*time.Minute),
		}, 150 * time.Second},
		{"three close together share the hold at 8 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 30*time.Second),
			arrival("DAL2", "TEST", "KJFK", time.Minute),
			arrival("DAL3", "TEST", "KJFK", 20*time.Minute),
		}, 120 * time.Second},
		{"a burst closes up to 6 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 30*time.Second),
			arrival("DAL2", "TEST", "KJFK", time.Minute),
			arrival("DAL3", "TEST", "KJFK", 90*time.Second),
			arrival("DAL4", "TEST", "KJFK", 2*time.Minute),
		}, 90 * time.Second},
		{"another flow's arrivals don't crowd it", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "OTHER", "KJFK", 30*time.Second),
			arrival("DAL3", "OTHER", "KJFK", time.Minute),
		}, 150 * time.Second},
		{"arrivals at an airport switched off don't crowd it", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "TEST", "KFRG", 30*time.Second),
			arrival("DAL3", "TEST", "KFRG", time.Minute),
		}, 150 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.ArrivalLaunches = map[string]ArrivalLaunch{"TEST": {Time: launch, TAS: 240}}
			s.Schedule.Arrivals = tc.queue

			s.State.SimTime = launch.Add(tc.trail - time.Second)
			if s.arrivalFlowSpaced("TEST") {
				t.Errorf("launched sooner than %s behind the last arrival", tc.trail)
			}
			s.State.SimTime = launch.Add(tc.trail + time.Second)
			if !s.arrivalFlowSpaced("TEST") {
				t.Errorf("held longer than %s behind the last arrival", tc.trail)
			}
		})
	}
}

// Launching an arrival or overflight enters it in the sim as its flow's last
// launch. An arrival also becomes the last arrival that published arrivals
// are spaced in trail behind, with its true airspeed.
func TestLaunchInbound(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := NewTestSim(testLogger())
	s.State.SimTime = now
	earlier := ArrivalLaunch{Time: now.Add(-10 * time.Minute), TAS: 250}
	s.ArrivalLaunches = map[string]ArrivalLaunch{"TEST": earlier}
	pos := math.Point2LL{-73, 41}

	s.launchInbound("TEST", testInboundAircraft("DAL1", "B738", av.FlightTypeOverflight, pos))
	if _, ok := s.Aircraft["DAL1"]; !ok {
		t.Error("launched overflight is not in the sim")
	}
	if s.InboundLaunches["TEST"] != "DAL1" {
		t.Errorf("flow's last launch is %q, want the overflight DAL1", s.InboundLaunches["TEST"])
	}
	if s.ArrivalLaunches["TEST"] != earlier {
		t.Error("an overflight changed the flow's last arrival")
	}

	s.launchInbound("TEST", testInboundAircraft("DAL2", "B738", av.FlightTypeArrival, pos))
	if _, ok := s.Aircraft["DAL2"]; !ok {
		t.Error("launched arrival is not in the sim")
	}
	if s.InboundLaunches["TEST"] != "DAL2" {
		t.Errorf("flow's last launch is %q, want the arrival DAL2", s.InboundLaunches["TEST"])
	}
	last := s.ArrivalLaunches["TEST"]
	if last.Time != now {
		t.Errorf("flow's last arrival launched at %s, want %s", last.Time, now)
	}
	if last.TAS <= 290 || last.TAS >= 320 {
		t.Errorf("recorded %.0f knots, want the true airspeed of 250 knots at 12,000'", last.TAS)
	}
}

func testInboundAircraft(callsign av.ADSBCallsign, actype string, kind av.TypeOfFlight, pos math.Point2LL) *Aircraft {
	return &Aircraft{
		ADSBCallsign:   callsign,
		AircraftType:   actype,
		FlightRules:    av.FlightRulesIFR,
		TypeOfFlight:   kind,
		ArrivalAirport: "KJFK",
		Nav:            nav.Nav{FlightState: nav.FlightState{Position: pos, IAS: 250, Altitude: 12000}},
	}
}

func testScenarioArrival(callsign, group string, spawn Time) ScheduledArrival {
	e := testScheduledArrival(callsign, group, "KJFK", spawn)
	e.Source = TrafficSourceScenario
	return e
}

func testScenarioOverflight(callsign, group string, spawn Time) ScheduledOverflight {
	return ScheduledOverflight{
		ScheduledFlight: ScheduledFlight{Callsign: callsign, AircraftType: "B738",
			Source: TrafficSourceScenario, SpawnTime: spawn},
		Group: group,
	}
}

// An arrival or overflight that has been created waits at its spawn point
// until it is separated from its flow's last launch, of either kind: by the
// wake turbulence separation the pair's categories call for, but never by
// less than minimum radar separation, which is 5 miles en route. However
// long it waits pushes its flow's later scenario arrivals and overflights
// back by as much; other flows' stay put.
func TestReadyInboundWaitsForSeparation(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	spawn := math.Point2LL{-73, 41}
	north := func(nm float32) math.Point2LL { return math.Point2LL{spawn[0], spawn[1] + nm/60} }
	arrival, overflight := av.FlightTypeArrival, av.FlightTypeOverflight

	for _, tc := range []struct {
		name           string
		facility       string
		lead, trail    string
		leadKind, kind av.TypeOfFlight
		short, clear   float32
	}{
		{"a small behind an A380 needs 8 miles", "N90", "A388", "C172", arrival, arrival, 7.9, 8.1},
		{"a large behind a lower heavy needs 3.5 miles", "N90", "B763", "B738", arrival, arrival, 3.4, 3.6},
		{"a pair needing no wake separation needs 3 miles", "N90", "B738", "B738", arrival, arrival, 2.9, 3.1},
		{"en route, a large behind a lower heavy needs 5 miles", "ZAU", "B763", "B738", arrival, arrival, 4.9, 5.1},
		{"an overflight waits behind an arrival", "N90", "A388", "C172", arrival, overflight, 7.9, 8.1},
		{"an arrival waits behind an overflight", "N90", "A388", "C172", overflight, arrival, 7.9, 8.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(testLogger())
			s.State.Facility = tc.facility
			s.State.SimTime = now
			s.Schedule.ScenarioGeneratedUntil = now.Add(time.Hour)
			lead := testInboundAircraft("AAL1", tc.lead, tc.leadKind, north(tc.short))
			s.Aircraft[lead.ADSBCallsign] = lead
			s.InboundLaunches = map[string]av.ADSBCallsign{"TEST": "AAL1"}
			s.ReadyInbounds = map[string]ReadyInbound{
				"TEST": {Aircraft: testInboundAircraft("N123", tc.trail, tc.kind, spawn), Since: now},
			}
			s.Schedule.Arrivals = []ScheduledArrival{
				testScenarioArrival("DAL2", "TEST", now.Add(2*time.Minute)),
				testScenarioArrival("DAL3", "OTHER", now.Add(3*time.Minute)),
			}
			s.Schedule.Overflights = []ScheduledOverflight{
				testScenarioOverflight("DAL4", "TEST", now.Add(4*time.Minute)),
				testScenarioOverflight("DAL5", "OTHER", now.Add(5*time.Minute)),
			}

			s.launchReadyInbounds()
			if _, ok := s.Aircraft["N123"]; ok {
				t.Fatalf("launched %.1f miles behind the flow's last launch", tc.short)
			}

			s.State.SimTime = now.Add(30 * time.Second)
			lead.Nav.FlightState.Position = north(tc.clear)
			s.launchReadyInbounds()
			if _, ok := s.Aircraft["N123"]; !ok {
				t.Fatalf("still waiting %.1f miles behind the flow's last launch", tc.clear)
			}
			if _, ok := s.ReadyInbounds["TEST"]; ok {
				t.Error("launched aircraft is still waiting")
			}
			if s.InboundLaunches["TEST"] != "N123" {
				t.Errorf("flow's last launch is %q, want N123", s.InboundLaunches["TEST"])
			}

			spawns := make(map[string]Time)
			for _, e := range s.Schedule.Arrivals {
				spawns[e.Callsign] = e.SpawnTime
			}
			for _, e := range s.Schedule.Overflights {
				spawns[e.Callsign] = e.SpawnTime
			}
			for callsign, want := range map[string]time.Duration{
				"DAL2": 2*time.Minute + 30*time.Second, // the flow's next arrival
				"DAL4": 4*time.Minute + 30*time.Second, // and overflight
				"DAL3": 3 * time.Minute,                // another flow's
				"DAL5": 5 * time.Minute,
			} {
				if spawns[callsign] != now.Add(want) {
					t.Errorf("%s spawns at %s, want %s", callsign, spawns[callsign], now.Add(want))
				}
			}
		})
	}
}

// A flow with an aircraft waiting at its spawn point creates no other
// arrival or overflight until that one launches, however many have come due;
// other flows carry on.
func TestReadyInboundHoldsItsFlow(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := NewTestSim(testLogger())
	s.State.SimTime = now
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates: map[string]map[string]float32{
			"TEST":  {"KJFK": 30, "overflights": 10},
			"OTHER": {"KJFK": 30, "overflights": 10},
		},
	}
	ready := testInboundAircraft("N123", "C172", av.FlightTypeArrival, math.Point2LL{-73, 41})
	s.ReadyInbounds = map[string]ReadyInbound{"TEST": {Aircraft: ready, Since: now.Add(-time.Minute)}}
	s.Schedule.Arrivals = []ScheduledArrival{
		testScenarioArrival("DAL1", "TEST", now.Add(-30*time.Second)),
		testScenarioArrival("DAL2", "OTHER", now.Add(-20*time.Second)),
		testScenarioArrival("DAL3", "TEST", now),
	}
	s.Schedule.Overflights = []ScheduledOverflight{
		testScenarioOverflight("DAL4", "TEST", now.Add(-10*time.Second)),
		testScenarioOverflight("DAL5", "OTHER", now),
	}

	// DAL2 and DAL5 are taken up in their turn; creating them fails in this
	// bare test sim, which has no flows to fly, and so they leave the queue.
	s.spawnScheduledArrivals()
	s.spawnScheduledOverflights()

	var queued []string
	for _, e := range s.Schedule.Arrivals {
		queued = append(queued, e.Callsign)
	}
	for _, e := range s.Schedule.Overflights {
		queued = append(queued, e.Callsign)
	}
	if !slices.Equal(queued, []string{"DAL1", "DAL3", "DAL4"}) {
		t.Errorf("queue holds %v, want the TEST flow's flights waiting in order", queued)
	}
	if r := s.ReadyInbounds["TEST"]; r.Aircraft != ready {
		t.Error("flow's ready aircraft was replaced")
	}
}

// newReadyInboundTestSim returns a strip test sim that hands out squawks and
// flies the inbound flows TEST and OTHER automatically.
func newReadyInboundTestSim() *Sim {
	s := newStripTestSim()
	s.LocalCodePool = av.MakeLocalSquawkCodePool(av.LocalSquawkCodePoolSpecifier{})
	s.ERAMComputer = makeERAMComputer("ZNY", s.LocalCodePool)
	s.State.LaunchConfig = readyInboundTestConfig(TrafficSourceScenario)
	return s
}

func readyInboundTestConfig(source TrafficSource) LaunchConfig {
	return LaunchConfig{
		TrafficSource:             source,
		InboundFlowRateScale:      1,
		PublishedArrivalRateScale: 1,
		InboundFlowRates: map[string]map[string]float32{
			"TEST":  {"KJFK": 30, "overflights": 10},
			"OTHER": {"KJFK": 30, "overflights": 10},
		},
		InboundFlowEnabled: map[string]map[string]bool{
			"TEST":  {"KJFK": true, "overflights": true},
			"OTHER": {"KJFK": true, "overflights": true},
		},
	}
}

// waitAtSpawn has ac wait at its flow's spawn point holding what a created
// arrival or overflight holds: a squawk and a flight plan with a list index,
// and a strip when its owner is a human position. A human-owned flight
// plan stays unassociated until the aircraft enters the sim.
func waitAtSpawn(t *testing.T, s *Sim, group string, ac *Aircraft, owner TCP) {
	t.Helper()
	fp := FlightPlan{
		ACID:               ACID(ac.ADSBCallsign),
		PlanType:           RemoteEnroute,
		Rules:              av.FlightRulesIFR,
		TypeOfFlight:       ac.TypeOfFlight,
		TrackingController: owner,
	}
	if err := s.ERAMComputer.AssignSquawk(ac, &fp, s.Rand); err != nil {
		t.Fatalf("AssignSquawk: %v", err)
	}
	s.giveFlightStrip(&fp, owner)
	if err := s.associateAtSpawn(ac, fp); err != nil {
		t.Fatalf("associateAtSpawn: %v", err)
	}
	s.readyInbound(group, ac)
}

// Deleting all aircraft deletes the arrivals and overflights waiting at their
// spawn points too: their flight plans go, returning their squawks and list
// indices, and a human-owned one's strip goes with its flight plan.
func TestDeleteAllAircraftDiscardsReadyInbounds(t *testing.T) {
	s := newReadyInboundTestSim()
	indices := len(s.STARSComputer.AvailableIndices)
	pos := math.Point2LL{-73, 41}
	arr := testInboundAircraft("DAL1", "B738", av.FlightTypeArrival, pos)
	waitAtSpawn(t, s, "TEST", arr, "2A")
	ofl := testInboundAircraft("DAL2", "B738", av.FlightTypeOverflight, pos)
	waitAtSpawn(t, s, "OTHER", ofl, "14")
	if !slices.Equal(s.flightStripACIDsForTCW("A"), []ACID{"DAL1"}) {
		t.Fatal("human-owned waiting arrival has no strip")
	}

	if err := s.DeleteAllAircraft("A"); err != nil {
		t.Fatalf("DeleteAllAircraft: %v", err)
	}

	if len(s.ReadyInbounds) != 0 {
		t.Errorf("%d aircraft still waiting at their spawn points", len(s.ReadyInbounds))
	}
	if len(s.STARSComputer.FlightPlans) != 0 {
		t.Error("the human-owned arrival's flight plan is still there")
	}
	if acids := s.flightStripACIDsForTCW("A"); len(acids) != 0 {
		t.Errorf("strips %v are still there", acids)
	}
	if n := len(s.STARSComputer.AvailableIndices); n != indices {
		t.Errorf("%d list indices available, want all %d back", n, indices)
	}
	for _, ac := range []*Aircraft{arr, ofl} {
		if s.ERAMComputer.SquawkCodePool.IsAssigned(ac.Squawk) {
			t.Errorf("%s's squawk %s is still assigned", ac.ADSBCallsign, ac.Squawk)
		}
	}
}

// A launch config change discards the aircraft waiting at their spawn points
// that it no longer launches automatically, and all of them when the traffic
// source changes; the others keep waiting.
func TestLaunchConfigChangeDiscardsReadyInbounds(t *testing.T) {
	historical := TrafficSourceHistorical
	for _, tc := range []struct {
		name    string
		source  TrafficSource
		change  func(lc *LaunchConfig)
		waiting []string
	}{
		{"nothing changes", 0, func(lc *LaunchConfig) {}, []string{"OTHER", "TEST"}},
		{"arrivals switch to manual", 0, func(lc *LaunchConfig) { lc.ArrivalMode = LaunchManual }, []string{"OTHER"}},
		{"overflights switch to manual", 0, func(lc *LaunchConfig) { lc.OverflightMode = LaunchManual },
			[]string{"TEST"}},
		{"the arrival's airport stops", 0, func(lc *LaunchConfig) { lc.InboundFlowRates["TEST"]["KJFK"] = 0 },
			[]string{"OTHER"}},
		{"another airport's rate changes", 0, func(lc *LaunchConfig) { lc.InboundFlowRates["OTHER"]["KJFK"] = 20 },
			[]string{"OTHER", "TEST"}},
		{"the overflights stop", 0, func(lc *LaunchConfig) { lc.InboundFlowRates["OTHER"]["overflights"] = 0 },
			[]string{"TEST"}},
		{"the traffic source changes", 0, func(lc *LaunchConfig) { lc.TrafficSource = TrafficSourceTimetable }, nil},
		{"published: nothing changes", historical, func(lc *LaunchConfig) {}, []string{"OTHER", "TEST"}},
		{"published: the arrival's airport is switched off", historical,
			func(lc *LaunchConfig) { lc.InboundFlowEnabled["TEST"]["KJFK"] = false }, []string{"OTHER"}},
		{"published: the arrival rate scale drops to zero", historical,
			func(lc *LaunchConfig) { lc.PublishedArrivalRateScale = 0 }, []string{"OTHER"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newReadyInboundTestSim()
			s.State.LaunchConfig = readyInboundTestConfig(tc.source)
			pos := math.Point2LL{-73, 41}
			waitAtSpawn(t, s, "TEST", testInboundAircraft("DAL1", "B738", av.FlightTypeArrival, pos), "14")
			waitAtSpawn(t, s, "OTHER", testInboundAircraft("DAL2", "B738", av.FlightTypeOverflight, pos), "14")

			old := readyInboundTestConfig(tc.source)
			tc.change(&s.State.LaunchConfig)
			s.applyScheduleConfigChanges(&old, nil)

			if waiting := util.SortedMapKeys(s.ReadyInbounds); !slices.Equal(waiting, tc.waiting) {
				t.Errorf("flows %v have aircraft waiting, want %v", waiting, tc.waiting)
			}
		})
	}
}

// Due published arrivals in a flow that isn't spaced yet stay queued in
// order, and hold up no other flow's. Scenario arrivals are spaced by their
// rates and never wait for miles in trail.
func TestSpacedFlowHoldsOnlyItsOwnArrivals(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := NewTestSim(testLogger())
	s.State.SimTime = now
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates:   map[string]map[string]float32{"PUCKY1": {"KJFK": 7}, "MIP4": {"KLGA": 30}},
		InboundFlowEnabled: map[string]map[string]bool{"PUCKY1": {"KJFK": true}, "MIP4": {"KLGA": true}},
	}
	justLaunched := ArrivalLaunch{Time: now.Add(-30 * time.Second), TAS: 300}
	s.ArrivalLaunches = map[string]ArrivalLaunch{"PUCKY1": justLaunched}
	scenario := testScheduledArrival("AAL4", "PUCKY1", "KJFK", now)
	scenario.Source = TrafficSourceScenario
	dropped := testScheduledArrival("DAL5", "PUCKY1", "KJFK", now)
	dropped.DropReason = "no route"
	s.Schedule.Arrivals = []ScheduledArrival{
		testScheduledArrival("DAL1", "PUCKY1", "KJFK", now.Add(-5*time.Minute)),
		testScheduledArrival("DAL2", "MIP4", "KLGA", now.Add(-time.Minute)),
		testScheduledArrival("DAL3", "PUCKY1", "KJFK", now),
		scenario,
		dropped,
		testScheduledArrival("DAL6", "PUCKY1", "KFRG", now),
	}

	// DAL2 and AAL4 are taken up in their turn; creating them fails in this
	// bare test sim, which has no flows to fly, and so they leave the queue.
	s.spawnScheduledArrivals()

	var queued []string
	for _, e := range s.Schedule.Arrivals {
		queued = append(queued, e.Callsign)
	}
	if !slices.Equal(queued, []string{"DAL1", "DAL3"}) {
		t.Errorf("queue holds %v, want the PUCKY1 arrivals waiting in order", queued)
	}
	if s.discardedArrivals["KFRG"] != 1 {
		t.Error("blocked flow did not discard its disabled airport's arrival")
	}

	s.State.SimTime = now.Add(3 * time.Minute)
	s.spawnScheduledArrivals()
	if len(s.Schedule.Arrivals) != 0 {
		t.Error("flow remained blocked after spacing opened on a later tick")
	}
}

func BenchmarkHeldArrivals(b *testing.B) {
	for _, n := range []int{20, 50, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
			s := NewTestSim(testLogger())
			s.State.SimTime = now
			s.State.LaunchConfig = LaunchConfig{
				InboundFlowRates:   map[string]map[string]float32{"TEST": {"KJFK": 12}},
				InboundFlowEnabled: map[string]map[string]bool{"TEST": {"KJFK": true}},
			}
			s.ArrivalLaunches = map[string]ArrivalLaunch{"TEST": {Time: now, TAS: 240}}
			for i := range n {
				s.Schedule.Arrivals = append(s.Schedule.Arrivals,
					testScheduledArrival(fmt.Sprintf("DAL%d", i), "TEST", "KJFK", now.Add(-5*time.Minute)))
			}
			for b.Loop() {
				s.spawnScheduledArrivals()
			}
			if len(s.Schedule.Arrivals) != n {
				b.Fatal("held arrivals left the queue")
			}
		})
	}
}

// An inbound flight's ERAM hard altitude is what it was cleared to, since
// conflict alert takes a level flight to be free to go anywhere between its
// altitude and the hard altitude.
func TestSetInboundERAMAltitudes(t *testing.T) {
	restricted := func(fix string, ar av.AltitudeRestriction) av.Waypoint {
		wp := av.Waypoint{Fix: fix}
		wp.SetAltitudeRestriction(ar)
		return wp
	}
	// The BAUBB3 as the ZLA scenarios fly it: its bottom is 4000 at EZKEL.
	baubb := av.WaypointArray{
		restricted("TCUPS", av.MakeAtOrAboveAltitudeRestriction(26000)),
		restricted("BAUBB", av.MakeRangeAltitudeRestriction(11000, 13000)),
		{Fix: "STYFF"},
		restricted("EZKEL", av.MakeAtAltitudeRestriction(4000)),
	}

	for _, tc := range []struct {
		name              string
		wps               av.WaypointArray
		assigned, cleared float32
		expected          int
	}{
		{name: "assigned altitude", wps: baubb, assigned: 21000, expected: 21000},
		{name: "assigned wins over cleared", wps: baubb, assigned: 21000, cleared: 24000, expected: 21000},
		{name: "descend via, except maintain", wps: baubb, cleared: 24000, expected: 24000},
		{name: "descend via", wps: baubb, expected: 4000},
		{name: "no restrictions", wps: av.WaypointArray{{Fix: "GVE"}, {Fix: "BAILZ"}}, expected: 35000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fp FlightPlan
			fp.setInboundERAMAltitudes(tc.wps, tc.assigned, tc.cleared, 35000)
			if fp.AssignedAltitude != tc.expected {
				t.Errorf("assigned altitude %d, expected %d", fp.AssignedAltitude, tc.expected)
			}
			if fp.DataBlockAltitude() != tc.expected {
				t.Errorf("data block altitude %d, expected %d", fp.DataBlockAltitude(), tc.expected)
			}
		})
	}
}

// holdTestSim returns a sim whose TEST flow has one arrival, flying route
// worked by ZNY, a virtual controller, unless initial gives the controller
// it starts with. N90 is the human position.
func holdTestSim(t *testing.T, route string, initial ControlPosition) *Sim {
	t.Helper()
	b, _ := json.Marshal(route)
	var wps av.WaypointArray
	if err := json.Unmarshal(b, &wps); err != nil {
		t.Fatalf("%s: %v", route, err)
	}
	for i := range wps {
		wps[i].Location, _ = db.DB.LookupWaypoint(wps[i].Fix)
	}

	s := NewTestSim(testLogger())
	s.ControlPositions = map[TCP]*av.Controller{"ZNY": {}, "ZOB": {}, "N90": {}}
	s.ScenarioDefaultConsolidation = PositionConsolidation{"N90": nil}
	s.STARSComputer = makeSTARSComputer("N90")
	s.terrain = (&db.Terrain{}).MakeGrid()
	s.State.NmPerLongitude = 45
	s.State.InboundFlows = map[string]*av.InboundFlow{"TEST": {Arrivals: []av.Arrival{{
		Waypoints:         wps,
		InitialController: util.Select(initial != "", initial, "ZNY"),
		Airports:          []av.ICAOAirportCode{"KEWR"},
	}}}}
	s.State.ArrivalHoldFixes = s.findArrivalHoldFixes()
	return s
}

// spawnHoldTestArrival creates an arrival on the TEST flow at alt, joining
// the holding stack as a scheduled one would.
func spawnHoldTestArrival(t *testing.T, s *Sim, callsign av.ADSBCallsign, alt int) *Aircraft {
	t.Helper()
	arr := s.State.InboundFlows["TEST"].Arrivals[0]
	arr.InitialAltitudes = []int{alt}
	n := nav.MakeArrivalNav(callsign, &arr, "KATL", "KEWR", 35000, db.DB.AircraftPerformance["B738"],
		nav.BriefPilotDelays, false, s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel,
		s.State.SimTime.NavTime(), s.Rand, s.lg)
	if n == nil {
		t.Fatalf("%s: no nav", callsign)
	}
	ac := &Aircraft{ADSBCallsign: callsign, TypeOfFlight: av.FlightTypeArrival, ControllerFrequency: "ZNY",
		ArrivalAirport: "KEWR", Nav: *n}
	s.joinHoldingStack(ac, "TEST", 0)
	s.Aircraft[callsign] = ac
	return ac
}

func assignedAltitudeOf(ac *Aircraft) float32 {
	if alt := ac.Nav.Altitude.Assigned; alt != nil {
		return *alt
	}
	return 0
}

// An arrival is held at the first fix in the database past its spawn
// point, so long as no human controller has the track by then.
func TestArrivalHoldFix(t *testing.T) {
	for _, tc := range []struct {
		route   string
		initial ControlPosition
		fix     string
	}{
		{route: "HAYED RACKI PENNS", fix: "RACKI"},
		{route: "HAYED RACKI PENNS", initial: "N90"},
		{route: "HAYED/ho RACKI PENNS"},
		{route: "HAYED/hoN90 RACKI PENNS"},
		{route: "HAYED/hoZOB RACKI PENNS", fix: "RACKI"},
		{route: "HAYED RACKI/ho PENNS", fix: "RACKI"},
		{route: "HAYED _HAYED_RACKI RACKI PENNS", fix: "RACKI"},
		{route: "HAYED _HAYED_RACKI/ho RACKI PENNS"},
		{route: "HAYED/h090 RACKI PENNS"},
	} {
		s := holdTestSim(t, tc.route, tc.initial)
		if fix := s.State.ArrivalHoldFixes["TEST"][0]; fix != tc.fix {
			t.Errorf("%s (%s): held at %q, expected %q", tc.route, tc.initial, fix, tc.fix)
		}
	}
}

// Each arrival joins the stack at the top while those already there step
// down 1,000'; a full stack stops the arrivals that would join it from
// spawning. When the holding ends, they leave from the bottom up, each
// once the one before it is clear of the fix, and arrivals that spawn in
// the meantime still join the stack.
func TestHoldingStack(t *testing.T) {
	s := holdTestSim(t, "HAYED RACKI PENNS SWEET", "")
	s.State.LaunchConfig.ArrivalHolds = map[string]bool{"RACKI": true}

	var held []*Aircraft
	for i := range maxHeldArrivals {
		held = append(held, spawnHoldTestArrival(t, s, av.ADSBCallsign(fmt.Sprintf("DAL%d", i+1)), 12000))
		for j, ac := range held {
			if alt, want := assignedAltitudeOf(ac), float32(12000-1000*(i-j)); alt != want {
				t.Errorf("%d holding: %s assigned %.0f, expected %.0f", i+1, ac.ADSBCallsign, alt, want)
			}
			if !ac.Nav.HoldingAt("RACKI") {
				t.Errorf("%s not holding at RACKI", ac.ADSBCallsign)
			}
		}
	}
	hold := s.HoldingStacks["RACKI"].Hold
	if hold.TurnDirection != av.TurnRight || hold.LegMinutes != 0 || hold.LegLengthNM != 0 {
		t.Errorf("RACKI has no charted hold, but holding in %+v", hold)
	}

	if !s.holdingStackFull("TEST", 0) {
		t.Error("stack of five not full")
	}
	s.Schedule.Arrivals = []ScheduledArrival{{
		ScheduledFlight: ScheduledFlight{Callsign: "DAL9", AircraftType: "B738", DepartureAirport: "KATL",
			ArrivalAirport: "KEWR", SpawnTime: s.State.SimTime}, Group: "TEST"}}
	s.spawnScheduledArrivals()
	if len(s.Schedule.Arrivals) != 1 {
		t.Error("arrival for a full stack was spawned")
	}
	if extra := spawnHoldTestArrival(t, s, "DAL6", 12000); extra.Nav.HoldingAt("RACKI") {
		t.Error("sixth arrival is holding")
	}

	s.State.LaunchConfig.ArrivalHolds["RACKI"] = false
	fix := s.HoldingStacks["RACKI"].Location
	held[0].Nav.FlightState.Position = fix
	s.updateHoldingStacks()
	if held[0].Nav.HoldingAt("RACKI") || !held[1].Nav.HoldingAt("RACKI") {
		t.Error("the bottom arrival wasn't the only one to be released")
	}
	s.updateHoldingStacks()
	if !held[1].Nav.HoldingAt("RACKI") {
		t.Error("released the next arrival with the one before it still at the fix")
	}
	held[0].Nav.FlightState.Position = math.Offset2LL(fix, 90, minArrivalTrailNM+1, s.State.NmPerLongitude)
	s.updateHoldingStacks()
	if held[1].Nav.HoldingAt("RACKI") || !held[2].Nav.HoldingAt("RACKI") {
		t.Error("didn't release the next arrival once the one before it was clear of the fix")
	}

	// DAL2 is on its way out but still in the hold.
	late := spawnHoldTestArrival(t, s, "DAL7", 12000)
	for i, ac := range []*Aircraft{held[1], held[2], held[3], held[4], late} {
		if alt, want := assignedAltitudeOf(ac), float32(8000+1000*i); alt != want {
			t.Errorf("after DAL7 joined: %s assigned %.0f, expected %.0f", ac.ADSBCallsign, alt, want)
		}
	}

	s.deleteAircraft(held[3], DeleteByController)
	if slices.ContainsFunc(s.HoldingStacks["RACKI"].Aircraft, func(h HeldArrival) bool { return h.Callsign == "DAL4" }) {
		t.Error("deleted aircraft still in the stack")
	}

	for _, ac := range []*Aircraft{held[1], held[2], held[4], late} {
		ac.Nav.LeaveHold("RACKI")
	}
	s.updateHoldingStacks()
	if _, ok := s.HoldingStacks["RACKI"]; ok {
		t.Error("empty stack remains")
	}
	if ac := spawnHoldTestArrival(t, s, "DAL8", 12000); ac.Nav.HoldingAt("RACKI") {
		t.Error("arrival held after the stack emptied")
	}
}

// A stack starts at the altitude its arrival is to cross the fix at, holds
// as charted, and never goes below the charted minimum; a later arrival
// that spawns lower brings the stack down under it.
func TestHoldingStackAltitudes(t *testing.T) {
	s := holdTestSim(t, "RACKI PENNS/a11000 SWEET", "")
	s.State.LaunchConfig.ArrivalHolds = map[string]bool{"PENNS": true}
	dal1 := spawnHoldTestArrival(t, s, "DAL1", 12000)
	if alt := assignedAltitudeOf(dal1); alt != 11000 {
		t.Errorf("holding at %.0f, not the 11,000 restriction", alt)
	}
	if hold := s.HoldingStacks["PENNS"].Hold; hold.InboundCourse != 122 || hold.LegMinutes != 1 {
		t.Errorf("not holding as charted at PENNS: %+v", hold)
	}
	dal2 := spawnHoldTestArrival(t, s, "DAL2", 5000)
	if a1, a2 := assignedAltitudeOf(dal1), assignedAltitudeOf(dal2); a1 != 4000 || a2 != 5000 {
		t.Errorf("assigned %.0f and %.0f, expected 4000 and 5000", a1, a2)
	}
	if !s.holdingStackFull("TEST", 0) {
		t.Error("stack not full above the charted 3,100' minimum")
	}
}

// Held arrivals enter the hold and fly it until released; then the bottom
// one leaves first and continues along its route.
func TestHeldArrivalsLeaveBottomFirst(t *testing.T) {
	s := holdTestSim(t, "HAYED RACKI PENNS SWEET", "")
	s.State.LaunchConfig.ArrivalHolds = map[string]bool{"RACKI": true}
	pRACKI, _ := db.DB.LookupWaypoint("RACKI")

	step := func(seconds int) {
		for range seconds {
			s.State.SimTime = s.State.SimTime.Add(time.Second)
			for ac := range util.SortedMapValues(s.Aircraft) {
				ac.Nav.UpdateWithWeather(string(ac.ADSBCallsign), wx.MakeStandardSampleForAltitude(ac.Altitude()),
					nil, av.FlightRulesIFR, s.State.SimTime.NavTime(), false)
			}
			s.updateHoldingStacks()
		}
	}
	stepUntil := func(what string, done func() bool) {
		for range 1800 {
			if done() {
				return
			}
			step(1)
		}
		t.Fatalf("%s never happened", what)
	}

	dal1 := spawnHoldTestArrival(t, s, "DAL1", 12000)
	step(90)
	dal2 := spawnHoldTestArrival(t, s, "DAL2", 12000)
	stepUntil("DAL2 reached RACKI", func() bool {
		return dal2.Nav.Heading.Hold != nil && math.NMDistance2LL(dal2.Position(), pRACKI) < 1
	})
	step(600)
	if dal1.Nav.Heading.Hold == nil || math.Abs(dal1.Altitude()-11000) > 50 || math.Abs(dal2.Altitude()-12000) > 50 {
		t.Fatalf("not holding at 11,000 and 12,000: %.0f, %.0f", dal1.Altitude(), dal2.Altitude())
	}

	s.State.LaunchConfig.ArrivalHolds["RACKI"] = false
	stepUntil("DAL1 left the hold", func() bool { return !dal1.Nav.HoldingAt("RACKI") })
	if !dal2.Nav.HoldingAt("RACKI") {
		t.Error("DAL2 left the hold with DAL1")
	}
	if d := math.NMDistance2LL(dal1.Position(), pRACKI); d > 1 {
		t.Errorf("DAL1 left the hold %.1fnm from RACKI", d)
	}
	stepUntil("DAL2 left the hold", func() bool { return !dal2.Nav.HoldingAt("RACKI") })
	if d := math.NMDistance2LL(dal1.Position(), pRACKI); d < minArrivalTrailNM {
		t.Errorf("DAL2 left the hold %.1fnm behind DAL1", d)
	}
	if dal1.Nav.Waypoints[0].Fix != "PENNS" {
		t.Errorf("DAL1 is flying to %s, not PENNS", dal1.Nav.Waypoints[0].Fix)
	}
}
