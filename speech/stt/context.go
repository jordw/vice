// speech/stt/context.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech"
)

// Aircraft holds context for a single aircraft for STT processing. Zero
// means "unknown"/"none" throughout, so the JSON tags omit empty values:
// this struct is logged verbatim with every transmission and stored in the
// test corpus, where zeroes are pure noise.
type Aircraft struct {
	Callsign                  string
	LastAddressed             bool                         `json:",omitempty"` // Most recent recipient on the user's TCW frequency
	AircraftType              string                       `json:",omitempty"` // Aircraft type code (e.g., "C172", "BE36")
	Fixes                     map[string]string            `json:",omitempty"` // spoken name -> fix ID
	CandidateApproaches       map[string]string            `json:",omitempty"` // canonical name ("RNAV Z Runway 28R") -> approach ID
	CandidateVisualApproaches map[string]string            `json:",omitempty"` // spoken name -> runway ID for active plain visual approaches
	ApproachFixes             map[string]map[string]string `json:",omitempty"` // approach ID -> (spoken name -> fix ID)
	ReportingPoints           map[string]string            `json:",omitempty"` // spoken name -> identifier, for the expected charted visual's reporting points ahead
	AssignedApproach          string                       `json:",omitempty"`
	ExpectedDirectFix         string                       `json:",omitempty"` // Fix the controller said to "expect direct" (if any)
	SID                       string                       `json:",omitempty"`
	STAR                      string                       `json:",omitempty"`
	Route                     []string                     `json:",omitempty"` // Ordered route waypoint fix names (full route, no truncation)
	Altitude                  int                          `json:",omitempty"` // Current altitude in feet
	Heading                   int                          `json:",omitempty"` // Current magnetic heading in degrees (0 if unknown)
	Speed                     int                          `json:",omitempty"` // Current groundspeed in knots (0 if unknown)
	AssignedAltitude          int                          `json:",omitempty"` // Controller-assigned altitude in feet (0 if none)
	AssignedHeading           int                          `json:",omitempty"` // Controller-assigned heading in degrees (0 if none)
	AssignedSpeed             av.Airspeed                  `json:",omitzero"`  // Controller-assigned speed in knots or Mach (zero if none)
	State                     string                       `json:",omitempty"` // "departure", "arrival", "cleared approach", "overflight", "vfr flight following"
	ControllerFrequency       string                       `json:",omitempty"` // Current controller position the aircraft is tuned to
	TrackingController        string                       `json:",omitempty"` // Controller tracking this aircraft (from flight plan)
	AddressingForm            sim.CallsignAddressingForm   `json:",omitempty"` // How this aircraft was addressed (based on which key matched)
	LAHSORunways              []string                     `json:",omitempty"` // Runways that intersect the approach runway (for LAHSO matching)
}

// Flight states, as BuildAircraftContext sets them in Aircraft.State.
const (
	stateDeparture       = "departure"
	stateArrival         = "arrival"
	stateClearedApproach = "cleared approach"
	stateOverflight      = "overflight"
	stateVFR             = "vfr flight following"
)

// DecodeFromState decodes a transcript using the simulation state directly.
// It builds the aircraft context internally from the provided state.
// Only tracks on the user's frequency (ControllerFrequency) are included in the context.
func (t *Transcriber) DecodeFromState(state *sim.UserState, userTCW sim.TCW, transcript string) (string, error) {
	return t.DecodeTranscript(t.BuildAircraftContext(state, userTCW), transcript, "")
}

// BuildAircraftContext creates the STT aircraft context from simulation state.
func (t *Transcriber) BuildAircraftContext(
	state *sim.UserState,
	userTCW sim.TCW,
) map[string]Aircraft {
	acCtx := make(map[string]Aircraft)
	lastAddressedCallsign := string(state.LastSTTCallsigns[userTCW])

	for _, trk := range state.Tracks {
		// Check if the aircraft is on the user's frequency
		if !state.TCWControlsPosition(userTCW, trk.ControllerFrequency) {
			continue
		}

		sttAc := Aircraft{
			Callsign:            string(trk.ADSBCallsign),
			LastAddressed:       lastAddressedCallsign == string(trk.ADSBCallsign),
			Altitude:            int(trk.TrueAltitude),
			Heading:             int(trk.Heading),
			Speed:               int(trk.Groundspeed),
			AssignedHeading:     trk.AssignedHeading,
			AssignedSpeed:       trk.AssignedSpeed,
			ControllerFrequency: string(trk.ControllerFrequency),
			Route:               trk.RouteFixes,
			ExpectedDirectFix:   trk.ExpectedDirectFix,
		}

		// Add tracking controller, aircraft type, and assigned altitude from
		// the flight plan.
		if trk.FlightPlan != nil {
			sttAc.TrackingController = string(trk.FlightPlan.TrackingController)
			sttAc.AircraftType = trk.FlightPlan.AircraftType
			sttAc.AssignedAltitude = trk.FlightPlan.AssignedAltitude
		}

		// Build fixes map
		sttAc.Fixes = make(map[string]string)
		for _, fix := range trk.Fixes {
			// For 4-letter ICAO airports, add all telephony variants so STT can match any of them.
			// For 3-letter identifiers, prefer navaid lookup (via GetFixTelephony) since these
			// are typically VORs, not airports. This avoids collisions where a 3-letter VOR
			// identifier matches a foreign airport ICAO code.
			if len(fix) == 4 {
				if variants := speech.GetAirportTelephonyVariants(fix); len(variants) > 0 {
					for _, variant := range variants {
						sttAc.Fixes[variant] = fix
					}
					continue
				}
			}
			sttAc.Fixes[speech.GetFixTelephony(fix)] = fix
		}

		if len(trk.ReportingPoints) > 0 {
			sttAc.ReportingPoints = make(map[string]string)
			for _, rp := range trk.ReportingPoints {
				for _, name := range rp.Names {
					sttAc.ReportingPoints[strings.ToLower(name)] = rp.Id
				}
			}
		}

		// Determine state and set SID/STAR
		if trk.IsDeparture() {
			sttAc.State = "departure"
			sttAc.SID = trk.SID
			sttAc.STAR = trk.STAR // Local departures may have a STAR
		} else if trk.IsArrival() {
			if trk.ClearedForApproach {
				sttAc.State = "cleared approach"
			} else {
				sttAc.State = "arrival"
			}
			sttAc.STAR = trk.STAR
		} else if trk.IsOverflight() {
			sttAc.State = "overflight"
		} else if trk.RequestedFlightFollowing {
			sttAc.State = "vfr flight following"
		}

		// Build candidate approaches for arrivals and local departures
		// (departures arriving at a TRACON airport need approach commands too)
		_, arrivingLocally := state.ArrivalAirports[trk.ArrivalAirport]
		if trk.ArrivalAirport != "" && (trk.IsArrival() || arrivingLocally) {
			sttAc.CandidateApproaches = make(map[string]string)
			sttAc.CandidateVisualApproaches = make(map[string]string)
			sttAc.ApproachFixes = make(map[string]map[string]string)
			if trk.Approach != "" {
				sttAc.AssignedApproach = trk.Approach
			}
			// Track runways we've seen to avoid duplicate LAHSO runway entries
			seenRunways := make(map[string]bool)
			// Add active approaches for the arrival airport
			for _, ar := range state.ArrivalRunways {
				if ar.Airport != trk.ArrivalAirport {
					continue
				}
				if ap, ok := state.Airports[ar.Airport]; ok {
					visualRunway := ar.Runway.Base()
					for _, phrase := range visualApproachTelephonyVariants(visualRunway) {
						sttAc.CandidateVisualApproaches[phrase] = visualRunway
					}

					for code, appr := range ap.Approaches {
						if appr.Runway == ar.Runway.Base() {
							sttAc.CandidateApproaches[appr.FullName] = code

							// Build fixes map for this approach
							approachFixes := make(map[string]string)
							for _, wps := range appr.Waypoints {
								for _, wp := range wps {
									if av.IsNamedFix(wp.Fix) {
										approachFixes[speech.GetFixTelephony(wp.Fix)] = wp.Fix
									}
								}
							}
							if len(approachFixes) > 0 {
								sttAc.ApproachFixes[code] = approachFixes
							}
						}
					}
					// Build LAHSORunways for this arrival runway (avoid duplicates)
					if !seenRunways[ar.Runway.Base()] {
						seenRunways[ar.Runway.Base()] = true
						intersecting := av.IntersectingRunways(db.Lookups{}, ar.Airport, ar.Runway, state.NmPerLongitude, 0.5)
						sttAc.LAHSORunways = append(sttAc.LAHSORunways, intersecting...)
					}
				}
			}
		}

		// Key by telephony (spoken callsign). Use the true CWT category
		// from the aircraft performance DB rather than the NAS flight plan,
		// since the user may have changed the flight plan's aircraft type.
		telephony := speech.GetCallsignSpoken(string(trk.ADSBCallsign), trk.CWTCategory)

		// Default addressing form is full callsign
		sttAc.AddressingForm = sim.AddressingFormFull
		acCtx[telephony] = sttAc

		// For GA callsigns (N-prefix), also add type-based addressing variants
		callsign := string(trk.ADSBCallsign)
		if strings.HasPrefix(callsign, "N") && sttAc.AircraftType != "" {
			typePronunciations := speech.GetACTypePronunciations(sttAc.AircraftType)
			if len(typePronunciations) > 0 {
				trailing3 := speech.GetTrailing3Spoken(callsign)
				if trailing3 != "" {
					// Create a copy with TypeTrailing3 addressing form.
					// Bake /T into the callsign so downstream code gets it automatically.
					typeAc := sttAc
					typeAc.Callsign += "/T"
					typeAc.AddressingForm = sim.AddressingFormTypeTrailing3
					typeAc.LastAddressed = lastAddressedCallsign == typeAc.Callsign

					// Add entry for each pronunciation variant that doesn't contain numbers
					// (to avoid confusion with other callsigns)
					for _, typeSpoken := range typePronunciations {
						if strings.ContainsAny(typeSpoken, "0123456789") {
							continue
						}
						key := typeSpoken + " " + trailing3
						acCtx[key] = typeAc
					}
				}
			}
		}
	}

	return acCtx
}

// spokenRunway returns the spoken form of a runway: "31L" is "three one
// left".
func spokenRunway(rwy string) string {
	var parts []string
	for _, ch := range strings.ToLower(rwy) {
		if ch >= '0' && ch <= '9' {
			parts = append(parts, speechDigits[ch-'0'])
		} else if side, ok := sideWords[string(ch)]; ok {
			parts = append(parts, side)
		}
	}
	return strings.Join(parts, " ")
}

// speechDigits are the digits as radiotelephony says them.
var speechDigits = [10]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "niner"}

// visualApproachTelephonyVariants returns the ways a visual approach to a
// runway is spoken.
func visualApproachTelephonyVariants(rwy string) []string {
	spoken := spokenRunway(rwy)
	return []string{"visual runway " + spoken, "visual approach runway " + spoken, "visual " + spoken}
}
