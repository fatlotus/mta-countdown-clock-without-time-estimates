package mta

import (
	"strings"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/encoding/protowire"
)

// NyctTrackInfo contains the scheduled and actual track decoded from NYCT extension 1001.
type NyctTrackInfo struct {
	ScheduledTrack string
	ActualTrack    string
}

// ExtractNyctTrackInfo decodes the NyctStopTimeUpdate extension (field 1001) from
// gtfs.TripUpdate_StopTimeUpdate unknown fields using protowire.
func ExtractNyctTrackInfo(stu *gtfs.TripUpdate_StopTimeUpdate) NyctTrackInfo {
	if stu == nil {
		return NyctTrackInfo{}
	}
	raw := stu.ProtoReflect().GetUnknown()
	var info NyctTrackInfo
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
		if num == 1001 && typ == protowire.BytesType {
			extBytes, m := protowire.ConsumeBytes(raw)
			if m < 0 {
				break
			}
			raw = raw[m:]
			// Inside NyctStopTimeUpdate message:
			// optional string scheduled_track = 1;
			// optional string actual_track = 2;
			for len(extBytes) > 0 {
				subNum, subTyp, sn := protowire.ConsumeTag(extBytes)
				if sn < 0 {
					break
				}
				extBytes = extBytes[sn:]
				if subTyp == protowire.BytesType {
					valBytes, vm := protowire.ConsumeBytes(extBytes)
					if vm < 0 {
						break
					}
					extBytes = extBytes[vm:]
					switch subNum {
					case 1:
						info.ScheduledTrack = strings.TrimSpace(string(valBytes))
					case 2:
						info.ActualTrack = strings.TrimSpace(string(valBytes))
					}
				} else {
					sm := protowire.ConsumeFieldValue(subNum, subTyp, extBytes)
					if sm < 0 {
						break
					}
					extBytes = extBytes[sm:]
				}
			}
		} else {
			m := protowire.ConsumeFieldValue(num, typ, raw)
			if m < 0 {
				break
			}
			raw = raw[m:]
		}
	}
	return info
}

// ClassifyTrackType determines whether a physical track identifier corresponds to
// "LOCAL", "EXPRESS", or "" (unknown / unclassified).
//
// In NYC Subway conventions:
//
// B Division (IND / BMT - lettered lines A, B, C, D, E, F, G, J, L, M, N, Q, R, W, Z):
// Tracks use directional chaining where odd numbers are Southbound and even numbers are Northbound:
// Track 1 = Southbound Local (e.g., A1)
// Track 2 = Northbound Local (e.g., A2)
// Track 3 = Southbound Express (e.g., A3)
// Track 4 = Northbound Express (e.g., A4)
// -> Tracks 1 and 2 are LOCAL; Tracks 3 and 4 are EXPRESS.
//
// A Division (IRT - numbered lines 1, 2, 3, 4, 5, 6, 7):
// Tracks are numbered consecutively across the roadbed (typically west to east):
// Track 1 = Southbound Local
// Track 2 = Southbound Express
// Track 3 = Northbound Express
// Track 4 = Northbound Local
// -> Tracks 1 and 4 are LOCAL; Tracks 2 and 3 are EXPRESS.
//
// Track M (or TK3/TKM on 3-track lines) = Peak Express across all divisions.
func ClassifyTrackType(track string, routeOrDivision ...string) string {
	t := strings.ToUpper(strings.TrimSpace(track))
	if t == "" {
		return ""
	}

	// Explicit text labels
	switch t {
	case "L", "LOC", "LOCAL":
		return "LOCAL"
	case "X", "EXP", "EXPRESS":
		return "EXPRESS"
	}

	// Peak express middle track
	if t == "M" || t == "TKM" || t == "TM" || strings.HasSuffix(t, "M") {
		return "EXPRESS"
	}

	// Check if this route/division or track prefix belongs to B Division (IND / BMT)
	isB := true
	if len(routeOrDivision) > 0 && routeOrDivision[0] != "" {
		rd := strings.ToUpper(strings.TrimSpace(routeOrDivision[0]))
		switch rd {
		case "IRT", "A DIVISION", "A_DIVISION":
			isB = false
		case "1", "2", "3", "4", "5", "6", "6X", "7", "7X":
			isB = false
		default:
			isB = true
		}
	} else {
		// If no division/route context was passed, check for B-Division chaining prefixes (e.g., A1-A4, B1-B4, D1-D4, F1-F4)
		if len(t) >= 2 && t[0] >= 'A' && t[0] <= 'Z' && t[0] != 'T' {
			isB = true
		}
	}

	if isB {
		// B Division (IND / BMT):
		// Tracks 1 & 2 are Local
		// Tracks 3 & 4 are Express
		if strings.HasSuffix(t, "1") || strings.HasSuffix(t, "2") {
			return "LOCAL"
		}
		if strings.HasSuffix(t, "3") || strings.HasSuffix(t, "4") {
			return "EXPRESS"
		}
	} else {
		// A Division (IRT):
		// Tracks 1 & 4 are Local
		// Tracks 2 & 3 are Express
		if strings.HasSuffix(t, "1") || strings.HasSuffix(t, "4") {
			return "LOCAL"
		}
		if strings.HasSuffix(t, "2") || strings.HasSuffix(t, "3") {
			return "EXPRESS"
		}
	}

	if strings.Contains(t, "LOC") {
		return "LOCAL"
	}
	if strings.Contains(t, "EXP") {
		return "EXPRESS"
	}

	return ""
}

// TrunkCorridorRoutes expands a station's active corridor routes to include all routes
// belonging to that physical line/trunk corridor family. This ensures express routes
// running local (e.g., A on 8th Ave, 2 on 7th Ave, 4 on Lex, D on 6th Ave) are properly
// included on local station boards and multi-track diagrams during service disruptions.
func TrunkCorridorRoutes(routes []string) []string {
	seen := map[string]bool{}
	var result []string
	add := func(r string) {
		if !seen[r] && r != "" {
			seen[r] = true
			result = append(result, r)
		}
	}
	for _, r := range routes {
		add(r)
	}

	for _, r := range routes {
		switch r {
		case "A", "C", "E":
			add("A")
			add("C")
			add("E")
		case "1", "2", "3":
			add("1")
			add("2")
			add("3")
		case "4", "5", "6", "6X":
			add("4")
			add("5")
			add("6")
			add("6X")
		case "7", "7X":
			add("7")
			add("7X")
		case "B", "D", "F", "M", "FX":
			add("B")
			add("D")
			add("F")
			add("M")
			add("FX")
		case "N", "Q", "R", "W":
			add("N")
			add("Q")
			add("R")
			add("W")
		case "J", "Z":
			add("J")
			add("Z")
		case "L":
			add("L")
		case "G":
			add("G")
		case "SI", "SIR":
			add("SI")
		}
	}
	return result
}
