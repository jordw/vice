// sim/nas_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// A surface tracking filter discards the reports of an unassociated target
// inside it unless the target squawks an SPC.
func TestSurfaceTrackingFilterVisibility(t *testing.T) {
	s := newHandoffTestSim()
	s.State.FacilityAdaptation.Filters.SurfaceTracking = FilterRegions{{AirspaceVolume: bigCircle()}}
	ac, _ := newHandoffTestTrack(s, "TST400")

	if !s.isRadarVisible(ac) {
		t.Error("associated track inside the filter is not visible")
	}

	ac.DisassociateFlightPlan()
	if s.isRadarVisible(ac) {
		t.Error("unassociated track inside the filter is visible")
	}

	ac.Squawk = 0o7700
	ac.Mode = av.TransponderModeAltitude
	if !s.isRadarVisible(ac) {
		t.Error("unassociated track squawking 7700 inside the filter is not visible")
	}

	ac.Mode = av.TransponderModeStandby
	if s.isRadarVisible(ac) {
		t.Error("unassociated track in standby inside the filter is visible")
	}
}

// An arrival whose arrival drop filter starts inside the surface tracking
// filter is dropped there and then loses its track.
func TestArrivalDropInsideSurfaceTrackingFilter(t *testing.T) {
	s := newHandoffTestSim()
	s.STARSComputer = makeSTARSComputer("TEST")
	s.State.FacilityAdaptation.Filters.SurfaceTracking = FilterRegions{{AirspaceVolume: bigCircle()}}
	s.State.FacilityAdaptation.Filters.ArrivalDrop = FilterRegions{{AirspaceVolume: bigCircle()}}
	ac, fp := newHandoffTestTrack(s, "TST400")

	s.STARSComputer.Update(s)

	if ac.IsAssociated() {
		t.Fatal("arrival inside the arrival drop filter is still associated")
	}
	if fp.DeleteTime.IsZero() {
		t.Error("dropped flight plan has no delete time")
	}
	if s.isRadarVisible(ac) {
		t.Error("dropped arrival inside the surface tracking filter is visible")
	}
}

// An FDAM region's TCP-specific leader direction goes on the flight plan for
// each of its pointout TCPs when a track enters the region. It comes off when
// the track leaves unless the region retains it.
func TestFDAMLeaderLineDirections(t *testing.T) {
	nw := math.NorthWest
	inside := map[ControlPosition]math.CardinalOrdinalDirection{"1B": nw, "1C": nw}

	for _, retain := range []bool{false, true} {
		s := newHandoffTestSim()
		ac, fp := newHandoffTestTrack(s, "TST400")
		region := FDAMRegion{
			AirspaceVolume:                   bigCircle(),
			NewTCPSpecificLeaderDirection:    &nw,
			PointoutTCPs:                     []ControlPosition{"1B", "1C"},
			RetainTCPSpecificLeaderDirection: retain,
		}
		region.Id = "F1"
		s.State.FacilityAdaptation.Filters.FDAM = FDAMRegions{region}

		s.processFDAMRegions(ac)
		if !maps.Equal(fp.FDAMLeaderLineDirections, inside) {
			t.Errorf("retain=%v: inside the region, directions = %v, want %v", retain,
				fp.FDAMLeaderLineDirections, inside)
		}

		// Move the region far away so that the track leaves it.
		s.State.FacilityAdaptation.Filters.FDAM[0].Center.Point2LL = math.Point2LL{10, 10}
		s.processFDAMRegions(ac)
		want := inside
		if !retain {
			want = nil
		}
		if !maps.Equal(fp.FDAMLeaderLineDirections, want) {
			t.Errorf("retain=%v: after leaving the region, directions = %v, want %v", retain,
				fp.FDAMLeaderLineDirections, want)
		}
	}
}
