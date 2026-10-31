package mta

import (
	"fmt"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

// buildTrackMap constructs the vertical corridor diagram with converging/diverging Local & Express tracks,
// strictly filtered to the active corridor's routes (`corridorRoutes`), while attaching clickable `OtherLinks`
// to each station row so users can jump to any other line serving that station.
func (s *Service) buildTrackMap(st Station, corridorRoutes []string, validFeeds []*gtfs.FeedMessage, forceSim bool) TrackMapData {
	if len(corridorRoutes) == 0 {
		corridorRoutes = st.Routes
	}
	if len(corridorRoutes) == 0 {
		corridorRoutes = []string{"A", "C"}
	}

	var spine CorridorSpine
	var targetIdx int

	// Check if matching precomputed spine exists
	for _, sc := range s.static.StationCorridors[st.ID] {
		if slicesEqual(sc.TrunkRoutes, corridorRoutes) || slicesEqual(sc.ActiveRoutes, corridorRoutes) {
			if sp, ok := s.static.Spines[sc.SpineID]; ok {
				spine = sp
				targetIdx = sc.TargetIndex
				break
			}
		}
	}
	if spine.ID == "" {
		spine, targetIdx = s.buildStaticSpine(st, corridorRoutes)
	}

	nodes := make([]TrackDiagramNode, len(spine.Nodes))
	copy(nodes, spine.Nodes)
	for i := range nodes {
		nodes[i].IsTarget = (nodes[i].StopID == st.ID)
		nodes[i].OffsetFromFocus = i - targetIdx
	}

	northLabel := st.NorthLabel
	if northLabel == "" {
		northLabel = "Northbound"
	}
	southLabel := st.SouthLabel
	if southLabel == "" {
		southLabel = "Southbound"
	}

	lineName := s.corridorLineName(st, spine.CorridorRoutes, true)
	if lineName == "" {
		lineName = spine.LineName
	}
	if lineName == "" {
		lineName = st.Line
	}

	tmap := TrackMapData{
		LineName:        lineName,
		NorthLabel:      northLabel,
		SouthLabel:      southLabel,
		CorridorRoutes:  spine.CorridorRoutes,
		LocalRoutes:     spine.LocalRoutes,
		ExpressRoutes:   spine.ExpressRoutes,
		HasExpressTrack: spine.HasExpressTrack,
		TargetIndex:     targetIdx,
		Nodes:           nodes,
	}

	var trains []SystemTrain
	if forceSim || len(validFeeds) == 0 {
		trains = s.simulateSystemTrains()
	} else {
		trains, _ = s.parseFeedsToTrains(validFeeds)
	}

	tmap.Vehicles = s.projectTrackVehicles(tmap, trains)
	return tmap
}

// projectTrackVehicles projects system trains onto the track map corridor diagram.
func (s *Service) projectTrackVehicles(tmap TrackMapData, trains []SystemTrain) []TrackVehicle {
	allowedRoutesSet := make(map[string]bool, len(tmap.CorridorRoutes))
	for _, r := range tmap.CorridorRoutes {
		allowedRoutesSet[r] = true
		allowedRoutesSet[s.normalizeRouteID(r)] = true
	}

	spineIdxMap := make(map[string]int, len(tmap.Nodes))
	for i, node := range tmap.Nodes {
		spineIdxMap[node.StopID] = i
	}

	isExpressRoute := make(map[string]bool, len(tmap.ExpressRoutes))
	for _, r := range tmap.ExpressRoutes {
		isExpressRoute[r] = true
	}

	vehicles := []TrackVehicle{}

	for _, tr := range trains {
		if !allowedRoutesSet[tr.RouteID] && !allowedRoutesSet[s.normalizeRouteID(tr.RouteID)] {
			continue
		}

		currStopID := tr.CurrentStopID
		nodeIdx, onSpine := spineIdxMap[currStopID]
		if !onSpine {
			continue
		}

		trackType := ClassifyTrackType(tr.Track, tr.RouteID)
		if trackType == "" {
			trackType = tr.TrackType
		}
		if trackType == "" {
			if tmap.HasExpressTrack && (isExpressRoute[tr.RouteID] || defaultExpressRoutes[tr.RouteID]) {
				trackType = "EXPRESS"
			} else {
				trackType = "LOCAL"
			}
		}

		placement := "AT_STOP"
		diagramNodeIdx := nodeIdx
		fromStopID := currStopID
		toStopID := currStopID
		currStopName := tr.CurrentStop
		if currStopName == "" {
			currStopName = currStopID
		}
		statusLabel := fmt.Sprintf("At %s", currStopName)

		if tr.StatusType == "AT_STATION" || (tr.ArrDiff <= 20 && tr.DepDiff >= -25) {
			placement = "AT_STOP"
			diagramNodeIdx = nodeIdx
			statusLabel = fmt.Sprintf("Stopped at %s", currStopName)
			if tmap.Nodes[diagramNodeIdx].IsMergedTrack {
				trackType = "MERGED"
			} else if tmap.Nodes[diagramNodeIdx].HasExpressBypass {
				trackType = "LOCAL"
			}
		} else {
			// Moving / between stops
			placement = "BETWEEN_STOPS"
			statusLabel = fmt.Sprintf("En route to %s", currStopName)

			if tr.Direction == "S" {
				if nodeIdx > 0 {
					diagramNodeIdx = nodeIdx - 1
					fromStopID = tmap.Nodes[diagramNodeIdx].StopID
					toStopID = currStopID
				} else {
					placement = "AT_STOP"
					diagramNodeIdx = 0
				}
			} else {
				if nodeIdx < len(tmap.Nodes)-1 {
					diagramNodeIdx = nodeIdx
					fromStopID = tmap.Nodes[nodeIdx+1].StopID
					toStopID = currStopID
				} else {
					placement = "AT_STOP"
					diagramNodeIdx = nodeIdx
				}
			}

			if diagramNodeIdx >= 0 && diagramNodeIdx < len(tmap.Nodes) {
				if tmap.Nodes[diagramNodeIdx].NextSegmentTracks == "MERGED" {
					trackType = "MERGED"
				}
			}
		}

		vehicles = append(vehicles, TrackVehicle{
			TripID:         tr.TripID,
			RouteID:        tr.RouteID,
			RouteShortName: tr.RouteShortName,
			RouteColor:     tr.RouteColor,
			RouteTextColor: tr.RouteTextColor,
			Direction:      tr.Direction,
			TrackType:      trackType,
			Track:          tr.Track,
			Placement:      placement,
			StopID:         currStopID,
			FromStopID:     fromStopID,
			ToStopID:       toStopID,
			NodeIndex:      diagramNodeIdx,
			StatusLabel:    statusLabel,
			Destination:    tr.Destination,
		})
	}

	return vehicles
}
