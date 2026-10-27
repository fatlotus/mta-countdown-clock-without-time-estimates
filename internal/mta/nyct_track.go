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
// 4-track lines:
// Track 1 = Southbound Local
// Track 2 = Southbound Express
// Track 3 = Northbound Express
// Track 4 = Northbound Local
// Track M = Peak Express (Middle Track)
func ClassifyTrackType(track string) string {
	t := strings.ToUpper(strings.TrimSpace(track))
	if t == "" {
		return ""
	}
	switch t {
	case "1", "4", "TK1", "TK4", "T1", "T4", "L", "LOC", "LOCAL":
		return "LOCAL"
	case "2", "3", "M", "TK2", "TK3", "TKM", "T2", "T3", "TM", "X", "EXP", "EXPRESS":
		return "EXPRESS"
	}
	// Suffix matching for prefixed tracks like "A1", "B4", etc.
	if strings.HasSuffix(t, "1") || strings.HasSuffix(t, "4") {
		return "LOCAL"
	}
	if strings.HasSuffix(t, "2") || strings.HasSuffix(t, "3") || strings.HasSuffix(t, "M") {
		return "EXPRESS"
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
