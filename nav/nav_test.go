// nav/nav_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"os"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// Real JFK-area coordinates from the CIFP database.
// Point2LL is [longitude, latitude].
var testFixes = map[string]math.Point2LL{
	"CAMRN": {-73.8555, 40.0173},
	"VIDIO": {-73.5580, 40.3933},
	"CATOD": {-73.5373, 40.5426},
	"IGIDE": {-73.6539, 40.5958},
	"ROSLY": {-73.6358, 40.7972},
	"ZALPO": {-73.6938, 40.7233},
	"LEFER": {-73.5328, 40.8242},
	"HAUPT": {-73.4099, 40.7673},
	"DPK":   {-73.3037, 40.7918},
	"KORD":  {-87.9048, 41.9742}, // Chicago O'Hare, ~620nm from JFK area
}

var kjfkLocation = math.Point2LL{-73.779317, 40.639447}

// simTime is the base simulation time used across all tests.
var simTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	fixes := make(map[string]av.Fix)
	for name, loc := range testFixes {
		fixes[name] = av.Fix{Id: name, Location: loc}
	}

	// DPK is also a navaid (VOR)
	navaids := map[string]av.Navaid{
		"DPK": {Id: "DPK", Type: "VOR", Name: "DEER PARK", Location: testFixes["DPK"]},
	}

	av.DB = &av.StaticDatabase{
		Fixes:   fixes,
		Navaids: navaids,
		Airports: map[string]av.FAAAirport{
			"KJFK": {
				Id:        "KJFK",
				Name:      "JOHN F KENNEDY INTL",
				Elevation: 13,
				Location:  kjfkLocation,
			},
		},
		Callsigns:           make(map[string]string),
		AircraftPerformance: make(map[string]av.AircraftPerformance),
		AircraftTypeAliases: make(map[string]string),
		Airlines:            make(map[string]av.Airline),
		Airways:             make(map[string][]av.Airway),
		EnrouteHolds:        make(map[string][]av.Hold),
		TerminalHolds:       make(map[string]map[string][]av.Hold),
	}

	os.Exit(m.Run())
}

func makeTestPerf() av.AircraftPerformance {
	var perf av.AircraftPerformance
	perf.Speed.Min = 130
	perf.Speed.V2 = 140
	perf.Speed.Landing = 135
	perf.Speed.CruiseTAS = 460
	perf.Speed.MaxTAS = 490
	perf.Rate.Climb = 2500
	perf.Rate.Descent = 2000
	perf.Rate.Accelerate = 5
	perf.Rate.Decelerate = 3
	perf.Turn.MaxBankAngle = 25
	perf.Turn.MaxBankRate = 3
	perf.Ceiling = 41000
	return perf
}

func makeTestNav() *Nav {
	route := []av.Waypoint{
		{Fix: "CAMRN", Location: testFixes["CAMRN"]},
		{Fix: "VIDIO", Location: testFixes["VIDIO"]},
		{Fix: "CATOD", Location: testFixes["CATOD"]},
		{Fix: "KJFK", Location: kjfkLocation},
	}

	return &Nav{
		FlightState: FlightState{
			Position:          testFixes["CAMRN"],
			Heading:           270,
			Altitude:          5000,
			IAS:               250,
			GS:                250,
			MagneticVariation: -13,
			NmPerLongitude:    45.5,
			ArrivalAirport:    av.Waypoint{Fix: "KJFK", Location: kjfkLocation},
			ArrivalAirportLocation:  kjfkLocation,
			ArrivalAirportElevation: 13,
		},
		Perf: makeTestPerf(),
		Rand:           rand.Make(),
		FixAssignments: make(map[string]NavFixAssignment),
		Waypoints:      route,
	}
}

// zeroWx returns a zero-value weather sample for tests that don't need wind.
func zeroWx() wx.Sample {
	return wx.Sample{}
}

///////////////////////////////////////////////////////////////////////////
// Deferred nav timing

func TestDeferredHeadingDoesNotFireEarly(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(180)
	turn := TurnClosest
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(10 * time.Second),
		Heading: &hdg,
		Turn:    &turn,
	}

	// Call TargetHeading at simTime — deferred is 10s in the future, should NOT fire
	nav.TargetHeading("TEST", zeroWx(), simTime)

	if nav.DeferredNavHeading == nil {
		t.Fatal("DeferredNavHeading was consumed too early")
	}
}

func TestDeferredHeadingFiresAfterDelay(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(180)
	turn := TurnClosest
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(-1 * time.Second), // already in the past
		Heading: &hdg,
		Turn:    &turn,
	}

	nav.TargetHeading("TEST", zeroWx(), simTime)

	if nav.DeferredNavHeading != nil {
		t.Fatal("DeferredNavHeading should have been consumed")
	}
	if nav.Heading.Assigned == nil || *nav.Heading.Assigned != 180 {
		t.Fatalf("expected heading 180, got %v", nav.Heading.Assigned)
	}
}

func TestDeferredDirectFixFiresAfterDelay(t *testing.T) {
	nav := makeTestNav()
	newWps := []av.Waypoint{
		{Fix: "CATOD", Location: testFixes["CATOD"]},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:      simTime.Add(-1 * time.Second),
		Waypoints: newWps,
	}

	nav.TargetHeading("TEST", zeroWx(), simTime)

	if nav.DeferredNavHeading != nil {
		t.Fatal("DeferredNavHeading should have been consumed")
	}
	if len(nav.Waypoints) != 2 || nav.Waypoints[0].Fix != "CATOD" {
		t.Fatalf("expected waypoints starting with CATOD, got %v", nav.Waypoints)
	}
}

func TestDeferredOnCourseFiresAfterDelay(t *testing.T) {
	nav := makeTestNav()
	// Put aircraft on a heading first
	hdg := float32(270)
	nav.Heading = NavHeading{Assigned: &hdg}

	// Enqueue on-course with time already passed
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time: simTime.Add(-1 * time.Second),
		// No heading, no waypoints => on-course
	}

	nav.TargetHeading("TEST", zeroWx(), simTime)

	if nav.DeferredNavHeading != nil {
		t.Fatal("DeferredNavHeading should have been consumed")
	}
	// On-course clears the assigned heading (sets it to nil since no Heading field in deferred)
	if nav.Heading.Assigned != nil {
		t.Fatalf("expected heading assignment to be cleared for on-course, got %v", *nav.Heading.Assigned)
	}
}

func TestNewCommandReplacesPendingDeferred(t *testing.T) {
	nav := makeTestNav()

	// First heading assignment
	nav.EnqueueHeading(180, TurnLeft, simTime)
	if nav.DeferredNavHeading == nil {
		t.Fatal("expected DeferredNavHeading after first enqueue")
	}

	// Second heading assignment should replace the first
	nav.EnqueueHeading(90, TurnRight, simTime)
	if nav.DeferredNavHeading == nil {
		t.Fatal("expected DeferredNavHeading after second enqueue")
	}
	if *nav.DeferredNavHeading.Heading != 90 {
		t.Fatalf("expected deferred heading 90, got %v", *nav.DeferredNavHeading.Heading)
	}
	if *nav.DeferredNavHeading.Turn != TurnRight {
		t.Fatalf("expected TurnRight, got %v", *nav.DeferredNavHeading.Turn)
	}
}

func TestClearedApproachShortensDeferredDelay(t *testing.T) {
	nav := makeTestNav()

	// Set up a deferred heading far in the future
	hdg := float32(180)
	turn := TurnClosest
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(30 * time.Second),
		Heading: &hdg,
		Turn:    &turn,
	}

	// Set up a minimal approach so ClearedApproach doesn't return "unable".
	// Put aircraft on a heading so prepareForApproach sets InterceptState
	// instead of trying to splice routes.
	assignedHdg := float32(270)
	nav.Heading = NavHeading{Assigned: &assignedHdg}
	nav.Approach.Assigned = &av.Approach{FullName: "ILS RWY 04L", Type: av.ILSApproach}
	nav.Approach.AssignedId = "I4L"

	beforeTime := nav.DeferredNavHeading.Time
	_, ok := nav.ClearedApproach("KJFK", "", false, simTime)
	if !ok {
		t.Fatal("ClearedApproach should have succeeded")
	}

	if nav.DeferredNavHeading == nil {
		t.Fatal("DeferredNavHeading should still exist after ClearedApproach")
	}
	if !nav.DeferredNavHeading.Time.Before(beforeTime) {
		t.Fatal("ClearedApproach should have shortened the deferred delay")
	}
}

///////////////////////////////////////////////////////////////////////////
// Deferred nav delay ranges

func TestEnqueueHeadingDelayRange(t *testing.T) {
	nav := makeTestNav()
	// No prior heading assignment — should get 5-9 second delay
	nav.EnqueueHeading(180, TurnLeft, simTime)

	delay := nav.DeferredNavHeading.Time.Sub(simTime)
	if delay < 5*time.Second || delay > 9*time.Second {
		t.Fatalf("expected delay in [5s, 9s], got %v", delay)
	}
}

func TestEnqueueHeadingAlreadyOnHeadingDelayRange(t *testing.T) {
	nav := makeTestNav()
	// Already on a heading — should get 3-6 second delay
	hdg := float32(270)
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.EnqueueHeading(180, TurnLeft, simTime)

	delay := nav.DeferredNavHeading.Time.Sub(simTime)
	if delay < 3*time.Second || delay > 6*time.Second {
		t.Fatalf("expected delay in [3s, 6s], got %v", delay)
	}
}

func TestEnqueueDirectFixDelayRange(t *testing.T) {
	nav := makeTestNav()
	// On a heading — should get 8-13 second delay
	hdg := float32(270)
	nav.Heading = NavHeading{Assigned: &hdg}
	wps := []av.Waypoint{{Fix: "CATOD", Location: testFixes["CATOD"]}}
	nav.EnqueueDirectFix(wps, simTime)

	delay := nav.DeferredNavHeading.Time.Sub(simTime)
	if delay < 8*time.Second || delay > 13*time.Second {
		t.Fatalf("expected delay in [8s, 13s], got %v", delay)
	}
}

func TestEnqueueOnCourseDelayRange(t *testing.T) {
	nav := makeTestNav()
	nav.EnqueueOnCourse(simTime)

	delay := nav.DeferredNavHeading.Time.Sub(simTime)
	if delay < 8*time.Second || delay > 13*time.Second {
		t.Fatalf("expected delay in [8s, 13s], got %v", delay)
	}
}

func TestClearedApproachShortenedDelayRange(t *testing.T) {
	nav := makeTestNav()

	// Set up a deferred heading 30s in the future
	hdg := float32(180)
	turn := TurnClosest
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(30 * time.Second),
		Heading: &hdg,
		Turn:    &turn,
	}

	assignedHdg := float32(270)
	nav.Heading = NavHeading{Assigned: &assignedHdg}
	nav.Approach.Assigned = &av.Approach{FullName: "ILS RWY 04L", Type: av.ILSApproach}
	nav.Approach.AssignedId = "I4L"

	nav.ClearedApproach("KJFK", "", false, simTime)

	// Shortened delay should be 3-6 seconds from simTime
	delay := nav.DeferredNavHeading.Time.Sub(simTime)
	if delay < 3*time.Second || delay > 6*time.Second {
		t.Fatalf("expected shortened delay in [3s, 6s], got %v", delay)
	}
}

///////////////////////////////////////////////////////////////////////////
// Heading commands

func TestAssignHeading(t *testing.T) {
	nav := makeTestNav()
	intent := nav.AssignHeading(180, TurnLeft, simTime)

	if nav.DeferredNavHeading == nil {
		t.Fatal("AssignHeading should create a DeferredNavHeading")
	}
	if nav.DeferredNavHeading.Heading == nil || *nav.DeferredNavHeading.Heading != 180 {
		t.Fatalf("expected deferred heading 180, got %v", nav.DeferredNavHeading.Heading)
	}

	hi, ok := intent.(av.HeadingIntent)
	if !ok {
		t.Fatalf("expected HeadingIntent, got %T", intent)
	}
	if hi.Heading != 180 {
		t.Fatalf("expected intent heading 180, got %v", hi.Heading)
	}
}

func TestFlyPresentHeading(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Heading = 270

	intent := nav.FlyPresentHeading(simTime)

	hi, ok := intent.(av.HeadingIntent)
	if !ok {
		t.Fatalf("expected HeadingIntent, got %T", intent)
	}
	if hi.Heading != 270 {
		t.Fatalf("expected present heading 270, got %v", hi.Heading)
	}
	if hi.Type != av.HeadingPresent {
		t.Fatalf("expected HeadingPresent type, got %v", hi.Type)
	}

	// Should also create a deferred heading with the current heading
	if nav.DeferredNavHeading == nil || nav.DeferredNavHeading.Heading == nil {
		t.Fatal("FlyPresentHeading should create a DeferredNavHeading")
	}
	if *nav.DeferredNavHeading.Heading != 270 {
		t.Fatalf("expected deferred heading 270, got %v", *nav.DeferredNavHeading.Heading)
	}
}

func TestAssignHeadingInvalidReturnsUnable(t *testing.T) {
	nav := makeTestNav()

	for _, hdg := range []float32{0, 400, -10} {
		intent := nav.AssignHeading(hdg, TurnClosest, simTime)
		if _, ok := intent.(av.UnableIntent); !ok {
			t.Errorf("heading %.0f: expected UnableIntent, got %T", hdg, intent)
		}
	}
}

func TestAssignHeading360IsValid(t *testing.T) {
	nav := makeTestNav()
	intent := nav.AssignHeading(360, TurnClosest, simTime)

	hi, ok := intent.(av.HeadingIntent)
	if !ok {
		t.Fatalf("expected HeadingIntent for heading 360, got %T", intent)
	}
	if hi.Heading != 360 {
		t.Fatalf("expected heading 360, got %v", hi.Heading)
	}
}

///////////////////////////////////////////////////////////////////////////
// Route commands

func TestDirectFixInRoute(t *testing.T) {
	nav := makeTestNav()
	intent := nav.DirectFix("CATOD", simTime)

	ni, ok := intent.(av.NavigationIntent)
	if !ok {
		t.Fatalf("expected NavigationIntent, got %T", intent)
	}
	if ni.Type != av.NavDirectFix {
		t.Fatalf("expected NavDirectFix, got %v", ni.Type)
	}
	if ni.Fix != "CATOD" {
		t.Fatalf("expected fix CATOD, got %v", ni.Fix)
	}

	if nav.DeferredNavHeading == nil {
		t.Fatal("DirectFix should create a DeferredNavHeading")
	}
	if len(nav.DeferredNavHeading.Waypoints) == 0 {
		t.Fatal("expected deferred waypoints for direct fix")
	}
	if nav.DeferredNavHeading.Waypoints[0].Fix != "CATOD" {
		t.Fatalf("expected first deferred waypoint CATOD, got %v", nav.DeferredNavHeading.Waypoints[0].Fix)
	}
}

func TestDirectFixNotInRoute(t *testing.T) {
	nav := makeTestNav()
	// ROSLY is a valid fix in the DB but not in our route
	intent := nav.DirectFix("ROSLY", simTime)

	ni, ok := intent.(av.NavigationIntent)
	if !ok {
		t.Fatalf("expected NavigationIntent, got %T", intent)
	}
	if ni.Type != av.NavDirectFix {
		t.Fatalf("expected NavDirectFix, got %v", ni.Type)
	}

	if nav.DeferredNavHeading == nil || len(nav.DeferredNavHeading.Waypoints) == 0 {
		t.Fatal("expected deferred waypoints for out-of-route direct fix")
	}
	// Should have ROSLY followed by KJFK (the arrival airport)
	wps := nav.DeferredNavHeading.Waypoints
	if wps[0].Fix != "ROSLY" {
		t.Fatalf("expected first waypoint ROSLY, got %v", wps[0].Fix)
	}
	if wps[len(wps)-1].Fix != "KJFK" {
		t.Fatalf("expected last waypoint KJFK, got %v", wps[len(wps)-1].Fix)
	}
}

func TestDirectFixInvalidFix(t *testing.T) {
	nav := makeTestNav()
	intent := nav.DirectFix("ZZZZZ", simTime)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for invalid fix, got %T", intent)
	}
}

func TestClimbViaSID(t *testing.T) {
	nav := makeTestNav()
	// Mark waypoints as OnSID
	for i := range nav.Waypoints {
		nav.Waypoints[i].OnSID = true
	}
	// Set altitude and speed assignments that ClimbViaSID should clear
	alt := float32(3000)
	nav.Altitude = NavAltitude{Assigned: &alt}
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	intent := nav.ClimbViaSID(simTime)

	pi, ok := intent.(av.ProcedureIntent)
	if !ok {
		t.Fatalf("expected ProcedureIntent, got %T", intent)
	}
	if pi.Type != av.ProcedureClimbViaSID {
		t.Fatalf("expected ProcedureClimbViaSID, got %v", pi.Type)
	}

	// Should have cleared altitude, speed, and enqueued on-course
	if nav.Altitude.Assigned != nil {
		t.Fatal("ClimbViaSID should clear assigned altitude")
	}
	if nav.Speed.Assigned != nil {
		t.Fatal("ClimbViaSID should clear assigned speed")
	}
	if nav.DeferredNavHeading == nil {
		t.Fatal("ClimbViaSID should enqueue on-course (DeferredNavHeading)")
	}
}

func TestDescendViaSTAR(t *testing.T) {
	nav := makeTestNav()
	// Mark waypoints as OnSTAR
	for i := range nav.Waypoints {
		nav.Waypoints[i].OnSTAR = true
	}
	alt := float32(10000)
	nav.Altitude = NavAltitude{Assigned: &alt}
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	intent := nav.DescendViaSTAR(simTime)

	pi, ok := intent.(av.ProcedureIntent)
	if !ok {
		t.Fatalf("expected ProcedureIntent, got %T", intent)
	}
	if pi.Type != av.ProcedureDescendViaSTAR {
		t.Fatalf("expected ProcedureDescendViaSTAR, got %v", pi.Type)
	}

	if nav.Altitude.Assigned != nil {
		t.Fatal("DescendViaSTAR should clear assigned altitude")
	}
	if nav.Speed.Assigned != nil {
		t.Fatal("DescendViaSTAR should clear assigned speed")
	}
	if nav.DeferredNavHeading == nil {
		t.Fatal("DescendViaSTAR should enqueue on-course (DeferredNavHeading)")
	}
}

func TestDepartOnCourse(t *testing.T) {
	nav := makeTestNav()
	// Put aircraft on an assigned heading so DepartOnCourse takes the full path
	hdg := float32(270)
	nav.Heading = NavHeading{Assigned: &hdg}

	nav.DepartOnCourse(8000, "CATOD", simTime)

	// Should have set altitude
	if nav.Altitude.Assigned == nil || *nav.Altitude.Assigned != 8000 {
		t.Fatalf("expected assigned altitude 8000, got %v", nav.Altitude.Assigned)
	}

	// Should have trimmed waypoints to start at the exit fix
	if len(nav.Waypoints) == 0 || nav.Waypoints[0].Fix != "CATOD" {
		t.Fatalf("expected waypoints starting at CATOD, got %v", nav.Waypoints)
	}

	// Should have enqueued on-course
	if nav.DeferredNavHeading == nil {
		t.Fatal("DepartOnCourse should enqueue on-course")
	}
}

func TestDepartOnCourseNotOnHeading(t *testing.T) {
	nav := makeTestNav()
	// No assigned heading — DepartOnCourse should just set altitude, not enqueue on-course
	nav.DepartOnCourse(8000, "CATOD", simTime)

	if nav.Altitude.Assigned == nil || *nav.Altitude.Assigned != 8000 {
		t.Fatalf("expected assigned altitude 8000, got %v", nav.Altitude.Assigned)
	}
	// Should NOT have enqueued on-course since we weren't on a heading
	if nav.DeferredNavHeading != nil {
		t.Fatal("DepartOnCourse without heading should not enqueue on-course")
	}
}

///////////////////////////////////////////////////////////////////////////
// Altitude commands

func TestAssignAltitude(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 5000

	intent := nav.AssignAltitude(10000, false)
	ai, ok := intent.(av.AltitudeIntent)
	if !ok {
		t.Fatalf("expected AltitudeIntent, got %T", intent)
	}
	if ai.Altitude != 10000 {
		t.Fatalf("expected altitude 10000, got %v", ai.Altitude)
	}
	if ai.Direction != av.AltitudeClimb {
		t.Fatalf("expected AltitudeClimb, got %v", ai.Direction)
	}
	if nav.Altitude.Assigned == nil || *nav.Altitude.Assigned != 10000 {
		t.Fatal("expected assigned altitude to be set to 10000")
	}
}

func TestAssignAltitudeDescend(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 10000

	intent := nav.AssignAltitude(3000, false)
	ai, ok := intent.(av.AltitudeIntent)
	if !ok {
		t.Fatalf("expected AltitudeIntent, got %T", intent)
	}
	if ai.Direction != av.AltitudeDescend {
		t.Fatalf("expected AltitudeDescend, got %v", ai.Direction)
	}
}

func TestAssignAltitudeMaintain(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 5000

	intent := nav.AssignAltitude(5000, false)
	ai, ok := intent.(av.AltitudeIntent)
	if !ok {
		t.Fatalf("expected AltitudeIntent, got %T", intent)
	}
	if ai.Direction != av.AltitudeMaintain {
		t.Fatalf("expected AltitudeMaintain, got %v", ai.Direction)
	}
}

func TestAssignAltitudeAboveCeiling(t *testing.T) {
	nav := makeTestNav()
	intent := nav.AssignAltitude(50000, false)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for altitude above ceiling, got %T", intent)
	}
}

///////////////////////////////////////////////////////////////////////////
// Speed commands

func TestAssignSpeed(t *testing.T) {
	nav := makeTestNav()
	intent := nav.AssignSpeed(200, false)

	si, ok := intent.(av.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent, got %T", intent)
	}
	if si.Speed != 200 {
		t.Fatalf("expected speed 200, got %v", si.Speed)
	}
	if nav.Speed.Assigned == nil || *nav.Speed.Assigned != 200 {
		t.Fatal("expected assigned speed to be set to 200")
	}
}

func TestAssignSpeedZeroCancels(t *testing.T) {
	nav := makeTestNav()
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	intent := nav.AssignSpeed(0, false)
	si, ok := intent.(av.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent, got %T", intent)
	}
	if si.Type != av.SpeedCancel {
		t.Fatalf("expected SpeedCancel, got %v", si.Type)
	}
	if nav.Speed.Assigned != nil {
		t.Fatal("speed 0 should clear assigned speed")
	}
}

func TestAssignSpeedBelowLanding(t *testing.T) {
	nav := makeTestNav()
	// Landing speed is 135
	intent := nav.AssignSpeed(100, false)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for speed below landing, got %T", intent)
	}
}

func TestAssignSpeedAboveMax(t *testing.T) {
	nav := makeTestNav()
	intent := nav.AssignSpeed(600, false)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for speed above max, got %T", intent)
	}
}

///////////////////////////////////////////////////////////////////////////
// State queries

func TestAssignedHeadingReturnsDeferred(t *testing.T) {
	nav := makeTestNav()

	// No heading assigned
	if _, ok := nav.AssignedHeading(); ok {
		t.Fatal("expected no assigned heading initially")
	}

	// Set a deferred heading
	hdg := float32(90)
	turn := TurnLeft
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(10 * time.Second),
		Heading: &hdg,
		Turn:    &turn,
	}

	h, ok := nav.AssignedHeading()
	if !ok {
		t.Fatal("expected assigned heading from deferred")
	}
	if h != 90 {
		t.Fatalf("expected 90, got %v", h)
	}
}

func TestAssignedHeadingReturnsActive(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(180)
	nav.Heading = NavHeading{Assigned: &hdg}

	h, ok := nav.AssignedHeading()
	if !ok {
		t.Fatal("expected assigned heading")
	}
	if h != 180 {
		t.Fatalf("expected 180, got %v", h)
	}
}

func TestAssignedWaypointsReturnsDeferred(t *testing.T) {
	nav := makeTestNav()
	deferredWps := []av.Waypoint{
		{Fix: "ROSLY", Location: testFixes["ROSLY"]},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:      simTime.Add(10 * time.Second),
		Waypoints: deferredWps,
	}

	wps := nav.AssignedWaypoints()
	if len(wps) != 2 || wps[0].Fix != "ROSLY" {
		t.Fatalf("expected deferred waypoints starting with ROSLY, got %v", wps)
	}
}

func TestAssignedWaypointsReturnsCurrent(t *testing.T) {
	nav := makeTestNav()
	wps := nav.AssignedWaypoints()
	if len(wps) != 4 || wps[0].Fix != "CAMRN" {
		t.Fatalf("expected current waypoints starting with CAMRN, got %v", wps)
	}
}

func TestIsAirborne(t *testing.T) {
	nav := makeTestNav()

	// V2 is 140; at 250 kts we should be airborne
	nav.FlightState.IAS = 250
	if !nav.IsAirborne() {
		t.Fatal("expected airborne at 250 kts IAS")
	}

	// Below V2
	nav.FlightState.IAS = 100
	if nav.IsAirborne() {
		t.Fatal("expected not airborne at 100 kts IAS")
	}
}

///////////////////////////////////////////////////////////////////////////
// Snapshot / rollback

func TestSnapshotRestore(t *testing.T) {
	nav := makeTestNav()
	alt := float32(5000)
	nav.Altitude = NavAltitude{Assigned: &alt}

	snap := nav.TakeSnapshot()

	// Modify nav state
	newAlt := float32(10000)
	nav.Altitude = NavAltitude{Assigned: &newAlt}
	hdg := float32(180)
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.Waypoints = nav.Waypoints[1:]

	// Restore
	nav.RestoreSnapshot(snap)

	if nav.Altitude.Assigned == nil || *nav.Altitude.Assigned != 5000 {
		t.Fatalf("expected altitude restored to 5000, got %v", nav.Altitude.Assigned)
	}
	if nav.Heading.Assigned != nil {
		t.Fatal("expected heading to be restored to nil")
	}
	if len(nav.Waypoints) != 4 {
		t.Fatalf("expected 4 waypoints after restore, got %d", len(nav.Waypoints))
	}
}

func TestSnapshotDoesNotRestoreFlightState(t *testing.T) {
	nav := makeTestNav()
	snap := nav.TakeSnapshot()

	// Modify flight state (physical aircraft state)
	nav.FlightState.Altitude = 15000
	nav.FlightState.Heading = 90

	nav.RestoreSnapshot(snap)

	// FlightState should NOT be restored
	if nav.FlightState.Altitude != 15000 {
		t.Fatalf("expected FlightState.Altitude to remain 15000, got %v", nav.FlightState.Altitude)
	}
	if nav.FlightState.Heading != 90 {
		t.Fatalf("expected FlightState.Heading to remain 90, got %v", nav.FlightState.Heading)
	}
}

///////////////////////////////////////////////////////////////////////////
// Unable edge cases

func TestClimbViaSIDNotOnSID(t *testing.T) {
	nav := makeTestNav()
	// Waypoints are NOT marked OnSID
	intent := nav.ClimbViaSID(simTime)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent when not on SID, got %T", intent)
	}
}

func TestDescendViaSTARNotOnSTAR(t *testing.T) {
	nav := makeTestNav()
	// Waypoints are NOT marked OnSTAR
	intent := nav.DescendViaSTAR(simTime)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent when not on STAR, got %T", intent)
	}
}

func TestResumeOwnNavigation(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(180)
	nav.Heading = NavHeading{Assigned: &hdg}

	// Also set up a deferred heading
	dhdg := float32(90)
	dturn := TurnRight
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(10 * time.Second),
		Heading: &dhdg,
		Turn:    &dturn,
	}

	intent := nav.ResumeOwnNavigation()

	ni, ok := intent.(av.NavigationIntent)
	if !ok {
		t.Fatalf("expected NavigationIntent, got %T", intent)
	}
	if ni.Type != av.NavResumeOwnNav {
		t.Fatalf("expected NavResumeOwnNav, got %v", ni.Type)
	}

	if nav.Heading.Assigned != nil {
		t.Fatal("ResumeOwnNavigation should clear assigned heading")
	}
	if nav.DeferredNavHeading != nil {
		t.Fatal("ResumeOwnNavigation should clear deferred heading")
	}

	// Should have waypoints remaining (path-finding trims to closest segment)
	if len(nav.Waypoints) == 0 {
		t.Fatal("ResumeOwnNavigation should preserve route waypoints")
	}
	// The route should end at KJFK
	if nav.Waypoints[len(nav.Waypoints)-1].Fix != "KJFK" {
		t.Fatalf("expected route to end at KJFK, got %v", nav.Waypoints[len(nav.Waypoints)-1].Fix)
	}
}

func TestResumeOwnNavigationNotOnHeading(t *testing.T) {
	nav := makeTestNav()
	// No heading assigned
	intent := nav.ResumeOwnNavigation()

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent when not on heading, got %T", intent)
	}
}

///////////////////////////////////////////////////////////////////////////
// GoAround

func TestGoAround(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Heading = 45
	nav.FlightState.ArrivalAirportElevation = 13

	// Set up state that GoAround should reset
	spd := float32(180)
	nav.Speed = NavSpeed{Assigned: &spd}
	nav.Approach = NavApproach{Cleared: true, AssignedId: "I4L"}
	hdg := float32(180)
	dhdgTurn := TurnLeft
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(5 * time.Second),
		Heading: &hdg,
		Turn:    &dhdgTurn,
	}

	nav.GoAround()

	// Should fly present heading
	if nav.Heading.Assigned == nil || *nav.Heading.Assigned != 45 {
		t.Fatalf("expected heading locked to 45, got %v", nav.Heading.Assigned)
	}

	// Deferred heading should be cleared
	if nav.DeferredNavHeading != nil {
		t.Fatal("GoAround should clear DeferredNavHeading")
	}

	// Speed should be cleared
	if nav.Speed.Assigned != nil {
		t.Fatal("GoAround should clear speed assignment")
	}

	// Approach should be cleared
	if nav.Approach.Cleared || nav.Approach.AssignedId != "" {
		t.Fatal("GoAround should clear approach state")
	}

	// Altitude should be set to truncated value: 1000 * int((13+2500)/1000) = 1000 * 2 = 2000
	if nav.Altitude.Assigned == nil || *nav.Altitude.Assigned != 2000 {
		t.Fatalf("expected go-around altitude 2000, got %v", nav.Altitude.Assigned)
	}

	// Waypoints should be just the arrival airport
	if len(nav.Waypoints) != 1 || nav.Waypoints[0].Fix != "KJFK" {
		t.Fatalf("expected waypoints to be just KJFK, got %v", nav.Waypoints)
	}
}

///////////////////////////////////////////////////////////////////////////
// DistanceAlongRoute

func TestDistanceAlongRoute(t *testing.T) {
	nav := makeTestNav()

	dist, err := nav.DistanceAlongRoute("CATOD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dist <= 0 {
		t.Fatalf("expected positive distance, got %v", dist)
	}

	// Distance to CATOD should be less than distance to KJFK
	distKJFK, err := nav.DistanceAlongRoute("KJFK")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if distKJFK <= dist {
		t.Fatalf("expected KJFK farther than CATOD: KJFK=%v CATOD=%v", distKJFK, dist)
	}
}

func TestDistanceAlongRouteFixNotInRoute(t *testing.T) {
	nav := makeTestNav()

	_, err := nav.DistanceAlongRoute("ROSLY")
	if err != ErrFixNotInRoute {
		t.Fatalf("expected ErrFixNotInRoute, got %v", err)
	}
}

func TestDistanceAlongRouteOnHeading(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(270)
	nav.Heading = NavHeading{Assigned: &hdg}

	_, err := nav.DistanceAlongRoute("CATOD")
	if err != ErrNotFlyingRoute {
		t.Fatalf("expected ErrNotFlyingRoute, got %v", err)
	}
}

///////////////////////////////////////////////////////////////////////////
// CrossFixAt

func TestCrossFixAtAltitude(t *testing.T) {
	nav := makeTestNav()
	alt := float32(8000)
	nav.Altitude = NavAltitude{Assigned: &alt}

	ar := &av.AltitudeRestriction{Range: [2]float32{3000, 3000}}
	intent := nav.CrossFixAt("VIDIO", ar, 0)

	ni, ok := intent.(av.NavigationIntent)
	if !ok {
		t.Fatalf("expected NavigationIntent, got %T", intent)
	}
	if ni.Type != av.NavCrossFixAt {
		t.Fatalf("expected NavCrossFixAt, got %v", ni.Type)
	}
	if ni.Fix != "VIDIO" {
		t.Fatalf("expected fix VIDIO, got %v", ni.Fix)
	}

	// Should have stored the altitude restriction in FixAssignments
	nfa, ok := nav.FixAssignments["VIDIO"]
	if !ok {
		t.Fatal("expected FixAssignment for VIDIO")
	}
	if nfa.Arrive.Altitude == nil {
		t.Fatal("expected altitude restriction in FixAssignment")
	}

	// CrossFixAt should clear other altitude assignments
	if nav.Altitude.Assigned != nil {
		t.Fatal("CrossFixAt should clear assigned altitude")
	}
}

func TestCrossFixAtSpeed(t *testing.T) {
	nav := makeTestNav()
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	intent := nav.CrossFixAt("VIDIO", nil, 210)

	ni, ok := intent.(av.NavigationIntent)
	if !ok {
		t.Fatalf("expected NavigationIntent, got %T", intent)
	}
	if ni.Speed == nil || *ni.Speed != 210 {
		t.Fatalf("expected intent speed 210, got %v", ni.Speed)
	}

	nfa := nav.FixAssignments["VIDIO"]
	if nfa.Arrive.Speed == nil || *nfa.Arrive.Speed != 210 {
		t.Fatalf("expected speed 210 in FixAssignment, got %v", nfa.Arrive.Speed)
	}

	// CrossFixAt should clear other speed assignments
	if nav.Speed.Assigned != nil {
		t.Fatal("CrossFixAt should clear assigned speed")
	}
}

func TestCrossFixAtNotInRoute(t *testing.T) {
	nav := makeTestNav()
	ar := &av.AltitudeRestriction{Range: [2]float32{3000, 3000}}
	intent := nav.CrossFixAt("ROSLY", ar, 0)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for fix not in route, got %T", intent)
	}
}

///////////////////////////////////////////////////////////////////////////
// AfterSpeed / AfterAltitude conditional paths

func TestAssignAltitudeAfterSpeed(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 5000
	nav.FlightState.IAS = 250

	// Assign a speed different from current IAS so the afterSpeed branch triggers
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	intent := nav.AssignAltitude(10000, true)
	ai, ok := intent.(av.AltitudeIntent)
	if !ok {
		t.Fatalf("expected AltitudeIntent, got %T", intent)
	}
	if ai.AfterSpeed == nil || *ai.AfterSpeed != 200 {
		t.Fatalf("expected AfterSpeed=200 in intent, got %v", ai.AfterSpeed)
	}

	// Should store in AfterSpeed fields, NOT in Assigned
	if nav.Altitude.Assigned != nil {
		t.Fatal("afterSpeed should not set Altitude.Assigned directly")
	}
	if nav.Altitude.AfterSpeed == nil || *nav.Altitude.AfterSpeed != 10000 {
		t.Fatalf("expected AfterSpeed altitude 10000, got %v", nav.Altitude.AfterSpeed)
	}
	if nav.Altitude.AfterSpeedSpeed == nil || *nav.Altitude.AfterSpeedSpeed != 200 {
		t.Fatalf("expected AfterSpeedSpeed 200, got %v", nav.Altitude.AfterSpeedSpeed)
	}
}

func TestAssignSpeedAfterAltitude(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 5000
	nav.FlightState.IAS = 250

	// Assign an altitude different from current so the afterAltitude branch triggers
	alt := float32(10000)
	nav.Altitude = NavAltitude{Assigned: &alt}

	intent := nav.AssignSpeed(200, true)
	si, ok := intent.(av.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent, got %T", intent)
	}
	if si.AfterAltitude == nil || *si.AfterAltitude != 10000 {
		t.Fatalf("expected AfterAltitude=10000 in intent, got %v", si.AfterAltitude)
	}

	// Should store in AfterAltitude fields, NOT in Assigned
	if nav.Speed.Assigned != nil {
		t.Fatal("afterAltitude should not set Speed.Assigned directly")
	}
	if nav.Speed.AfterAltitude == nil || *nav.Speed.AfterAltitude != 200 {
		t.Fatalf("expected AfterAltitude speed 200, got %v", nav.Speed.AfterAltitude)
	}
	if nav.Speed.AfterAltitudeAltitude == nil || *nav.Speed.AfterAltitudeAltitude != 10000 {
		t.Fatalf("expected AfterAltitudeAltitude 10000, got %v", nav.Speed.AfterAltitudeAltitude)
	}
}

///////////////////////////////////////////////////////////////////////////
// DirectFix too far away

func TestDirectFixTooFarAway(t *testing.T) {
	nav := makeTestNav()
	// KORD is ~620nm from the test position near JFK — well beyond 150nm limit
	intent := nav.DirectFix("KORD", simTime)

	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for fix too far away, got %T", intent)
	}
}

///////////////////////////////////////////////////////////////////////////
// TargetHeading

func TestTargetHeadingAssigned(t *testing.T) {
	nav := makeTestNav()
	hdg := float32(180)
	turn := TurnLeft
	nav.Heading = NavHeading{Assigned: &hdg, Turn: &turn}

	h, tm, _ := nav.TargetHeading("TEST", zeroWx(), simTime)
	if math.Abs(h-180) > 1 {
		t.Fatalf("expected heading ~180, got %v", h)
	}
	// The bank/turn logic should produce TurnLeft since we set that
	_ = tm
}

func TestTargetHeadingWaypointFollowing(t *testing.T) {
	nav := makeTestNav()
	// No assigned heading, should target first waypoint (CAMRN)
	// Aircraft is AT CAMRN, so first waypoint is already there.
	// Move aircraft slightly west so it has to fly toward CAMRN.
	nav.FlightState.Position = math.Point2LL{-74.0, 40.0173}

	h, _, _ := nav.TargetHeading("TEST", zeroWx(), simTime)
	// Should be heading roughly east toward CAMRN
	if h < 50 || h > 130 {
		t.Fatalf("expected heading roughly east toward CAMRN, got %v", h)
	}
}

func TestTargetHeadingEmptyWaypointsNoHeading(t *testing.T) {
	nav := makeTestNav()
	nav.Waypoints = nil
	nav.Heading = NavHeading{} // no assigned heading

	// Should return current heading without crashing
	h, _, _ := nav.TargetHeading("TEST", zeroWx(), simTime)
	if h != 270 {
		t.Fatalf("expected current heading 270, got %v", h)
	}
}

func TestTargetHeadingJoiningArc(t *testing.T) {
	nav := makeTestNav()
	nav.Heading = NavHeading{
		Arc: &av.DMEArc{
			Center:         kjfkLocation,
			Radius:         10,
			InitialHeading: 45,
			Clockwise:      true,
		},
		JoiningArc: true,
	}
	nav.FlightState.Heading = 270 // far from 45

	h, _, _ := nav.TargetHeading("TEST", zeroWx(), simTime)
	// Should target the arc's initial heading
	if math.Abs(h-45) > 1 {
		t.Fatalf("expected arc initial heading ~45, got %v", h)
	}
	// JoiningArc should still be true since we're not close to 45 yet
	if !nav.Heading.JoiningArc {
		t.Fatal("JoiningArc should still be true when heading difference > 1")
	}
}

func TestTargetHeadingJoiningArcClears(t *testing.T) {
	nav := makeTestNav()
	nav.Heading = NavHeading{
		Arc: &av.DMEArc{
			Center:         kjfkLocation,
			Radius:         10,
			InitialHeading: 270,
			Clockwise:      true,
		},
		JoiningArc: true,
	}
	nav.FlightState.Heading = 270 // already at the arc's initial heading

	nav.TargetHeading("TEST", zeroWx(), simTime)
	// JoiningArc should be cleared since we're close
	if nav.Heading.JoiningArc {
		t.Fatal("JoiningArc should be cleared when heading difference < 1")
	}
}

///////////////////////////////////////////////////////////////////////////
// TargetAltitude

func TestTargetAltitudeAssigned(t *testing.T) {
	nav := makeTestNav()
	alt := float32(10000)
	nav.Altitude = NavAltitude{Assigned: &alt}

	targetAlt, rate := nav.TargetAltitude()
	if targetAlt != 10000 {
		t.Fatalf("expected target altitude 10000, got %v", targetAlt)
	}
	if rate != MaximumRate {
		t.Fatalf("expected MaximumRate, got %v", rate)
	}
}

func TestTargetAltitudeNoAssignments(t *testing.T) {
	nav := makeTestNav()
	nav.Altitude = NavAltitude{} // nothing assigned

	targetAlt, rate := nav.TargetAltitude()
	if targetAlt != 5000 {
		t.Fatalf("expected current altitude 5000, got %v", targetAlt)
	}
	if rate != 0 {
		t.Fatalf("expected rate 0 (maintain), got %v", rate)
	}
}

func TestTargetAltitudeCleared(t *testing.T) {
	nav := makeTestNav()
	cleared := float32(8000)
	nav.Altitude = NavAltitude{Cleared: &cleared}
	nav.FinalAltitude = 10000

	targetAlt, rate := nav.TargetAltitude()
	// Should return min(cleared, FinalAltitude) = 8000
	if targetAlt != 8000 {
		t.Fatalf("expected cleared altitude 8000, got %v", targetAlt)
	}
	if rate != MaximumRate {
		t.Fatalf("expected MaximumRate, got %v", rate)
	}
}

func TestTargetAltitudeClearedCappedByFinalAltitude(t *testing.T) {
	nav := makeTestNav()
	cleared := float32(15000)
	nav.Altitude = NavAltitude{Cleared: &cleared}
	nav.FinalAltitude = 10000

	targetAlt, _ := nav.TargetAltitude()
	if targetAlt != 10000 {
		t.Fatalf("expected target capped at FinalAltitude 10000, got %v", targetAlt)
	}
}

func TestTargetAltitudeRestriction(t *testing.T) {
	nav := makeTestNav()
	// "At or above 6000" — aircraft at 5000 should target 6000
	nav.Altitude = NavAltitude{
		Restriction: &av.AltitudeRestriction{Range: [2]float32{6000, 0}},
	}
	nav.FlightState.Altitude = 5000

	targetAlt, rate := nav.TargetAltitude()
	if targetAlt != 6000 {
		t.Fatalf("expected restriction target 6000, got %v", targetAlt)
	}
	if rate != MaximumRate {
		t.Fatalf("expected MaximumRate, got %v", rate)
	}
}

func TestTargetAltitudeTakeoffRoll(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.InitialDepartureClimb = true
	nav.FlightState.IAS = 100 // below V2 (140), not airborne
	alt := float32(5000)
	nav.Altitude = NavAltitude{Assigned: &alt}

	_, rate := nav.TargetAltitude()
	if rate != 0 {
		t.Fatalf("expected rate 0 during takeoff roll, got %v", rate)
	}
}

// Waypoint altitude constraints

func TestTargetAltitudeWaypointConstraintClimbing(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 3000
	nav.FlightState.GS = 250
	// Set an "at or above 5000" restriction on the first waypoint
	nav.Waypoints[0].AltitudeRestriction = &av.AltitudeRestriction{Range: [2]float32{5000, 5000}}

	targetAlt, rate := nav.TargetAltitude()
	// Should climb immediately since below constraint
	if targetAlt != 5000 {
		t.Fatalf("expected target 5000 (climb to constraint), got %v", targetAlt)
	}
	if rate != MaximumRate {
		t.Fatalf("expected MaximumRate for climbing, got %v", rate)
	}
}

func TestTargetAltitudeWaypointConstraintClimbingClearedCeiling(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 3000
	nav.FlightState.GS = 250
	cleared := float32(4000)
	nav.Altitude = NavAltitude{Cleared: &cleared}
	nav.Waypoints[0].AltitudeRestriction = &av.AltitudeRestriction{Range: [2]float32{5000, 5000}}

	targetAlt, _ := nav.TargetAltitude()
	// Climb should be capped by cleared altitude
	if targetAlt != 4000 {
		t.Fatalf("expected target capped at cleared 4000, got %v", targetAlt)
	}
}

func TestTargetAltitudeWaypointConstraintDescendingTooEarly(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 10000
	nav.FlightState.GS = 250
	// Put constraint on a far-away waypoint so ETA is large and needed rate is small
	nav.Waypoints = []av.Waypoint{
		{Fix: "DPK", Location: testFixes["DPK"],
			AltitudeRestriction: &av.AltitudeRestriction{Range: [2]float32{8000, 8000}}},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	nav.FlightState.Position = testFixes["CAMRN"] // far from DPK

	targetAlt, rate := nav.TargetAltitude()
	// With a large ETA, needed rate should be small (< descent/2), so stay level
	if targetAlt != nav.FlightState.Altitude {
		t.Fatalf("expected to stay at current altitude %v, got target %v", nav.FlightState.Altitude, targetAlt)
	}
	if rate != 0 {
		t.Fatalf("expected rate 0 (stay level), got %v", rate)
	}
}

func TestTargetAltitudeWaypointConstraintDescendingTimeToDescend(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 10000
	nav.FlightState.GS = 250
	// Put constraint on a very close waypoint so needed descent rate is large
	// Use VIDIO and position the aircraft very close to it
	nav.Waypoints = []av.Waypoint{
		{Fix: "VIDIO", Location: testFixes["VIDIO"],
			AltitudeRestriction: &av.AltitudeRestriction{Range: [2]float32{4000, 4000}}},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	// Position just a few nm from VIDIO
	nav.FlightState.Position = math.Point2LL{testFixes["VIDIO"][0] - 0.05, testFixes["VIDIO"][1]}

	targetAlt, rate := nav.TargetAltitude()
	// Should start descending since the waypoint is close
	if targetAlt >= nav.FlightState.Altitude {
		t.Fatalf("expected target below current altitude, got target %v", targetAlt)
	}
	if rate <= 0 {
		t.Fatalf("expected positive descent rate, got %v", rate)
	}
}

func TestTargetAltitudePostFAFLinearDescent(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 2000
	nav.FlightState.GS = 150
	nav.Approach.PassedFAF = true
	nav.Approach.Cleared = true
	nav.Approach.Assigned = &av.Approach{FullName: "ILS RWY 04L", Type: av.ILSApproach}
	// Waypoint with altitude constraint — close by
	nav.Waypoints = []av.Waypoint{
		{Fix: "VIDIO", Location: testFixes["VIDIO"],
			AltitudeRestriction: &av.AltitudeRestriction{Range: [2]float32{1000, 1000}}},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	nav.FlightState.Position = testFixes["CAMRN"]

	targetAlt, rate := nav.TargetAltitude()
	// After FAF, should return the constraint altitude with a linear rate
	if targetAlt != 1000 {
		t.Fatalf("expected target 1000 post-FAF, got %v", targetAlt)
	}
	// Rate should be a computed linear rate, not MaximumRate
	if rate == MaximumRate || rate <= 0 {
		t.Fatalf("expected a computed linear descent rate post-FAF, got %v", rate)
	}
}

///////////////////////////////////////////////////////////////////////////
// TargetSpeed

func TestTargetSpeedAssigned(t *testing.T) {
	nav := makeTestNav()
	spd := float32(200)
	nav.Speed = NavSpeed{Assigned: &spd}

	targetSpeed, rate := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	if targetSpeed != 200 {
		t.Fatalf("expected target speed 200, got %v", targetSpeed)
	}
	if rate != MaximumRate {
		t.Fatalf("expected MaximumRate, got %v", rate)
	}
}

func TestTargetSpeedMaintainSlowestPractical(t *testing.T) {
	nav := makeTestNav()
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}

	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	expected := nav.Perf.Speed.Landing + 5
	if targetSpeed != expected {
		t.Fatalf("expected landing+5 = %v, got %v", expected, targetSpeed)
	}
}

func TestTargetSpeedMaintainMaximumForward(t *testing.T) {
	nav := makeTestNav()
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	nav.FlightState.Altitude = 5000

	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	// Not on approach, should return targetAltitudeIAS which is min(cruiseIAS, 250) at 5000ft
	cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, 5000)
	expected := min(cruiseIAS, 250)
	if math.Abs(targetSpeed-expected) > 1 {
		t.Fatalf("expected ~%v for max forward, got %v", expected, targetSpeed)
	}
}

func TestTargetSpeed10kLimit(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 10200
	nav.FlightState.IAS = 280
	nav.FlightState.GS = 280

	// Target altitude below 10k, IAS > 250 — should command 250
	// dalt = 200, salt = 200 / (2000/60) = 6s
	// dspeed = 30, sspeed = 30 / (3/2) = 20s
	// salt(6) <= sspeed(20), so it should command 250
	targetSpeed, _ := nav.TargetSpeed(3000, nil, zeroWx(), nil)
	if targetSpeed != 250 {
		t.Fatalf("expected 250 (10k speed limit), got %v", targetSpeed)
	}
}

func TestTargetSpeed10kLimitEarly(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 20000
	nav.FlightState.IAS = 280
	nav.FlightState.GS = 280

	// Far above 10k — plenty of time to slow; should NOT command 250 yet
	// With descent rate 2000fpm, 10000ft takes 300s
	// With decel rate 1.5kts/s, 30kts takes 20s
	// 300s > 20s, so it should not force 250
	targetSpeed, _ := nav.TargetSpeed(3000, nil, zeroWx(), nil)
	if targetSpeed == 250 {
		t.Fatalf("should not command 250 yet when far above 10k, got %v", targetSpeed)
	}
}

func TestTargetSpeedDefaultAbove10k(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.Altitude = 25000

	targetSpeed, _ := nav.TargetSpeed(25000, nil, zeroWx(), nil)
	// Above 10k, should lerp toward cruise speed
	cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, 25000)
	if targetSpeed < 250 || targetSpeed > cruiseIAS+1 {
		t.Fatalf("expected speed in [250, cruiseIAS=%v] above 10k, got %v", cruiseIAS, targetSpeed)
	}
}

func TestTargetSpeedInitialDepartureClimbJet(t *testing.T) {
	nav := makeTestNav()
	nav.Perf.Engine.AircraftType = "J"
	nav.FlightState.InitialDepartureClimb = true
	nav.FlightState.DepartureAirportElevation = 13
	nav.FlightState.IAS = 180
	nav.FlightState.GS = 180

	// Below 1500 AGL: should target 180
	nav.FlightState.Altitude = 1000 // AGL = 1000 - 13 = 987
	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	if targetSpeed != 180 {
		t.Fatalf("expected 180 below 1500 AGL for jet, got %v", targetSpeed)
	}

	// Above 1500 AGL but below 5000 AGL: should target 210
	nav.FlightState.Altitude = 2000 // AGL = 2000 - 13 = 1987
	targetSpeed, _ = nav.TargetSpeed(5000, nil, zeroWx(), nil)
	if targetSpeed != 210 {
		t.Fatalf("expected 210 above 1500 AGL for jet, got %v", targetSpeed)
	}
}

func TestTargetSpeedInitialDepartureClimbProp(t *testing.T) {
	nav := makeTestNav()
	nav.Perf.Engine.AircraftType = "P"
	nav.FlightState.InitialDepartureClimb = true
	nav.FlightState.DepartureAirportElevation = 0
	nav.FlightState.IAS = 160
	nav.FlightState.GS = 160

	v2 := nav.Perf.Speed.V2 // 140

	// Below 500 AGL: 1.1 * V2
	nav.FlightState.Altitude = 400
	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	expected := float32(1.1) * v2
	if math.Abs(targetSpeed-expected) > 1 {
		t.Fatalf("expected ~%v below 500 AGL for prop, got %v", expected, targetSpeed)
	}

	// 500-1000 AGL: 1.2 * V2
	nav.FlightState.Altitude = 700
	targetSpeed, _ = nav.TargetSpeed(5000, nil, zeroWx(), nil)
	expected = float32(1.2) * v2
	if math.Abs(targetSpeed-expected) > 1 {
		t.Fatalf("expected ~%v at 500-1000 AGL for prop, got %v", expected, targetSpeed)
	}

	// 1000-1500 AGL: 1.3 * V2
	nav.FlightState.Altitude = 1200
	targetSpeed, _ = nav.TargetSpeed(5000, nil, zeroWx(), nil)
	expected = float32(1.3) * v2
	if math.Abs(targetSpeed-expected) > 1 {
		t.Fatalf("expected ~%v at 1000-1500 AGL for prop, got %v", expected, targetSpeed)
	}
}

func TestTargetSpeedRestrictionFromPreviousWaypoint(t *testing.T) {
	nav := makeTestNav()
	spd := float32(210)
	nav.Speed = NavSpeed{Restriction: &spd}

	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	if targetSpeed != 210 {
		t.Fatalf("expected restriction speed 210, got %v", targetSpeed)
	}
}

func TestTargetSpeedHold(t *testing.T) {
	nav := makeTestNav()
	// Set up a hold on a nearby fix — position very close so ETA < 180s
	holdFix := testFixes["VIDIO"]
	nav.Heading.Hold = &FlyHold{
		FixLocation: holdFix,
		Hold: av.Hold{
			Fix:           "VIDIO",
			InboundCourse: 90,
			TurnDirection: av.TurnRight,
			LegMinutes:    1,
		},
	}
	// Position just 1-2nm from VIDIO so ETA is well under 3 minutes
	nav.FlightState.Position = math.Point2LL{testFixes["VIDIO"][0] - 0.02, testFixes["VIDIO"][1]}
	nav.FlightState.GS = 250

	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	// At 5000ft, hold speed is 200 (below 6000)
	if targetSpeed != 200 {
		t.Fatalf("expected hold speed 200, got %v", targetSpeed)
	}
}

func TestTargetSpeedUpcomingWaypointRestrictionOnSTAR(t *testing.T) {
	nav := makeTestNav()
	nav.FlightState.IAS = 250
	nav.FlightState.GS = 250
	nav.FlightState.Altitude = 5000
	// Set a speed restriction on a very close waypoint on the STAR
	// Position aircraft right next to VIDIO so ETA < 5s
	nav.Waypoints = []av.Waypoint{
		{Fix: "VIDIO", Location: testFixes["VIDIO"], Speed: 210, OnSTAR: true},
		{Fix: "CATOD", Location: testFixes["CATOD"]},
		{Fix: "KJFK", Location: kjfkLocation},
	}
	nav.FlightState.Position = math.Point2LL{testFixes["VIDIO"][0] - 0.002, testFixes["VIDIO"][1]}

	targetSpeed, _ := nav.TargetSpeed(5000, nil, zeroWx(), nil)
	// ETA < 5, so should return the speed restriction
	if targetSpeed != 210 {
		t.Fatalf("expected upcoming waypoint speed 210, got %v", targetSpeed)
	}
}
