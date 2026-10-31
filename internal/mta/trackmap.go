package mta

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

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
	spineIdxMap := make(map[string]int, len(nodes))
	for i := range nodes {
		nodes[i].IsTarget = (nodes[i].StopID == st.ID)
		nodes[i].OffsetFromFocus = i - targetIdx
		spineIdxMap[nodes[i].StopID] = i
	}

	allowedRoutesSet := make(map[string]bool, len(spine.CorridorRoutes))
	for _, r := range spine.CorridorRoutes {
		allowedRoutesSet[r] = true
		allowedRoutesSet[s.normalizeRouteID(r)] = true
	}
	isExpressRoute := make(map[string]bool, len(spine.ExpressRoutes))
	for _, r := range spine.ExpressRoutes {
		isExpressRoute[r] = true
	}

	var vehicles []TrackVehicle
	if forceSim || len(validFeeds) == 0 {
		vehicles = s.buildSimulatedTrackVehicles(st, nodes, spineIdxMap, spine.LocalRoutes, spine.ExpressRoutes, spine.HasExpressTrack)
	} else {
		vehicles = s.extractLiveTrackVehicles(nodes, spineIdxMap, allowedRoutesSet, isExpressRoute, spine.HasExpressTrack, validFeeds)
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

	return TrackMapData{
		LineName:        lineName,
		NorthLabel:      northLabel,
		SouthLabel:      southLabel,
		CorridorRoutes:  spine.CorridorRoutes,
		LocalRoutes:     spine.LocalRoutes,
		ExpressRoutes:   spine.ExpressRoutes,
		HasExpressTrack: spine.HasExpressTrack,
		TargetIndex:     targetIdx,
		Nodes:           nodes,
		Vehicles:        vehicles,
	}
}

func (s *Service) extractLiveTrackVehicles(
	nodes []TrackDiagramNode,
	spineIdxMap map[string]int,
	allowedRoutesSet map[string]bool,
	isExpressRoute map[string]bool,
	hasExpress bool,
	validFeeds []*gtfs.FeedMessage,
) []TrackVehicle {
	var vehicles []TrackVehicle
	seenTrips := map[string]bool{}

	for _, feed := range validFeeds {
		feedTS := int64(feed.GetHeader().GetTimestamp())
		if feedTS == 0 {
			feedTS = time.Now().Unix()
		}

		vehMap := make(map[string]*gtfs.VehiclePosition)
		for _, entity := range feed.GetEntity() {
			if v := entity.GetVehicle(); v != nil && v.GetTrip().GetTripId() != "" {
				vehMap[v.GetTrip().GetTripId()] = v
			}
		}

		for _, entity := range feed.GetEntity() {
			tu := entity.GetTripUpdate()
			if tu == nil {
				continue
			}
			routeID := tu.GetTrip().GetRouteId()
			// Strictly filter to trains belonging to the currently selected track corridor!
			if !allowedRoutesSet[routeID] && !allowedRoutesSet[s.normalizeRouteID(routeID)] {
				continue
			}

			tripID := tu.GetTrip().GetTripId()
			if seenTrips[tripID] {
				continue
			}
			stus := tu.GetStopTimeUpdate()
			if len(stus) == 0 {
				continue
			}

			currIdx := -1
			for i, stu := range stus {
				t := stu.GetDeparture().GetTime()
				if t == 0 {
					t = stu.GetArrival().GetTime()
				}
				if t == 0 || t >= feedTS-25 {
					currIdx = i
					break
				}
			}
			if currIdx == -1 {
				continue
			}

			if currIdx == 0 {
				firstTime := stus[0].GetDeparture().GetTime()
				if firstTime == 0 {
					firstTime = stus[0].GetArrival().GetTime()
				}
				if firstTime-feedTS > 600 {
					continue
				}
			}

			rawStopID := stus[currIdx].GetStopId()
			currStopID := CleanStopID(rawStopID)
			nodeIdx, onSpine := spineIdxMap[currStopID]
			if !onSpine {
				continue
			}

			dir := "S"
			if strings.HasSuffix(rawStopID, "N") {
				dir = "N"
			} else if strings.HasSuffix(rawStopID, "S") {
				dir = "S"
			} else if currIdx+1 < len(stus) {
				nextID := CleanStopID(stus[currIdx+1].GetStopId())
				if nextNodeIdx, ok := spineIdxMap[nextID]; ok && nextNodeIdx < nodeIdx {
					dir = "N"
				}
			}

			rInfo := s.getRouteInfo(routeID)

			currTrackInfo := ExtractNyctTrackInfo(stus[currIdx])
			nextTrackInfo := NyctTrackInfo{}
			if currIdx+1 < len(stus) {
				nextTrackInfo = ExtractNyctTrackInfo(stus[currIdx+1])
			}

			reportedTrack := currTrackInfo.ActualTrack
			if reportedTrack == "" {
				reportedTrack = nextTrackInfo.ActualTrack
			}
			if reportedTrack == "" {
				reportedTrack = currTrackInfo.ScheduledTrack
			}
			if reportedTrack == "" {
				reportedTrack = nextTrackInfo.ScheduledTrack
			}

			trackType := ClassifyTrackType(reportedTrack, routeID)

			if trackType == "" {
				if hasExpress && (isExpressRoute[routeID] || defaultExpressRoutes[routeID]) {
					trackType = "EXPRESS"
				} else {
					trackType = "LOCAL"
				}
			}

			veh := vehMap[tripID]
			arrTime := stus[currIdx].GetArrival().GetTime()
			depTime := stus[currIdx].GetDeparture().GetTime()
			if arrTime == 0 {
				arrTime = depTime
			}
			if depTime == 0 {
				depTime = arrTime
			}
			arrDiff := arrTime - feedTS
			depDiff := depTime - feedTS
			vehAtCurr := veh != nil && CleanStopID(veh.GetStopId()) == currStopID

			currStopName := s.stopNames[currStopID]
			placement := "AT_STOP"
			diagramNodeIdx := nodeIdx
			fromStopID := currStopID
			toStopID := currStopID
			statusLabel := fmt.Sprintf("At %s", currStopName)

			if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_STOPPED_AT) || (arrDiff <= 20 && depDiff >= -25) {
				placement = "AT_STOP"
				diagramNodeIdx = nodeIdx
				statusLabel = fmt.Sprintf("Stopped at %s", currStopName)
				if nodes[diagramNodeIdx].IsMergedTrack {
					trackType = "MERGED"
				} else if nodes[diagramNodeIdx].HasExpressBypass {
					trackType = "LOCAL"
				}
			} else {
				prevSpineIdx := nodeIdx - 1
				if dir == "N" {
					prevSpineIdx = nodeIdx + 1
				}
				if prevSpineIdx >= 0 && prevSpineIdx < len(nodes) {
					placement = "BETWEEN_STOPS"
					fromStopID = nodes[prevSpineIdx].StopID
					toStopID = currStopID
					if dir == "S" {
						diagramNodeIdx = nodeIdx - 1
					} else {
						diagramNodeIdx = nodeIdx
					}
					if diagramNodeIdx >= 0 && diagramNodeIdx < len(nodes) && nodes[diagramNodeIdx].NextSegmentTracks == "MERGED" {
						trackType = "MERGED"
					}
					if arrDiff <= 45 {
						statusLabel = fmt.Sprintf("Approaching %s (from %s)", currStopName, nodes[prevSpineIdx].Name)
					} else {
						statusLabel = fmt.Sprintf("Between %s & %s", nodes[prevSpineIdx].Name, currStopName)
					}
				} else {
					placement = "AT_STOP"
					statusLabel = fmt.Sprintf("Approaching %s", currStopName)
					if nodes[diagramNodeIdx].IsMergedTrack {
						trackType = "MERGED"
					} else if nodes[diagramNodeIdx].HasExpressBypass {
						trackType = "LOCAL"
					}
				}
			}

			termStopID := CleanStopID(stus[len(stus)-1].GetStopId())
			destName := s.stopNames[termStopID]
			if destName == "" {
				destName = termStopID
			}

			seenTrips[tripID] = true
			vehicles = append(vehicles, TrackVehicle{
				TripID:         tripID,
				RouteID:        routeID,
				RouteShortName: rInfo.ShortName,
				RouteColor:     rInfo.Color,
				RouteTextColor: rInfo.TextColor,
				Direction:      dir,
				TrackType:      trackType,
				Track:          reportedTrack,
				Placement:      placement,
				StopID:         currStopID,
				FromStopID:     fromStopID,
				ToStopID:       toStopID,
				NodeIndex:      diagramNodeIdx,
				StatusLabel:    statusLabel,
				Destination:    destName,
			})
		}
	}

	return vehicles
}

func (s *Service) buildSimulatedTrackVehicles(
	st Station,
	nodes []TrackDiagramNode,
	spineIdxMap map[string]int,
	localRoutes []string,
	expressRoutes []string,
	hasExpress bool,
) []TrackVehicle {
	var vehicles []TrackVehicle
	targetIdx := spineIdxMap[st.ID]
	timeBucket := time.Now().Unix() / 15
	rng := rand.New(rand.NewSource(timeBucket + int64(len(nodes))))
	_ = rng

	allRoutes := append([]string{}, localRoutes...)
	allRoutes = append(allRoutes, expressRoutes...)
	if len(allRoutes) == 0 {
		allRoutes = []string{"A", "C"}
	}

	relativeOffsets := []int{-7, -5, -3, -2, -1, 0, 1, 3, 5, 7}
	for _, dir := range []string{"N", "S"} {
		for idx, rel := range relativeOffsets {
			nodeIdx := targetIdx + rel
			if nodeIdx < 0 || nodeIdx >= len(nodes) {
				continue
			}
			routeID := allRoutes[(idx+int(dir[0]))%len(allRoutes)]
			rInfo := s.getRouteInfo(routeID)

			trackType := "LOCAL"
			if hasExpress && containsStr(expressRoutes, routeID) {
				trackType = "EXPRESS"
			}
			if nodes[nodeIdx].HasExpressBypass {
				trackType = "LOCAL"
			}

			placement := "AT_STOP"
			segIdx := nodeIdx
			currStop := nodes[nodeIdx]
			fromStopID := currStop.StopID
			toStopID := currStop.StopID
			statusLabel := fmt.Sprintf("Stopped at %s", currStop.Name)

			if (idx+int(timeBucket))%2 == 1 && nodeIdx+1 < len(nodes) {
				placement = "BETWEEN_STOPS"
				segIdx = nodeIdx
				if nodes[segIdx].NextSegmentTracks == "MERGED" {
					trackType = "MERGED"
				}
				if dir == "S" {
					fromStopID = nodes[nodeIdx].StopID
					toStopID = nodes[nodeIdx+1].StopID
					statusLabel = fmt.Sprintf("Between %s & %s", nodes[nodeIdx].Name, nodes[nodeIdx+1].Name)
				} else {
					fromStopID = nodes[nodeIdx+1].StopID
					toStopID = nodes[nodeIdx].StopID
					statusLabel = fmt.Sprintf("Between %s & %s", nodes[nodeIdx+1].Name, nodes[nodeIdx].Name)
				}
			} else {
				if nodes[nodeIdx].IsMergedTrack {
					trackType = "MERGED"
				}
			}

			destNode := nodes[len(nodes)-1]
			if dir == "N" {
				destNode = nodes[0]
			}

			vehicles = append(vehicles, TrackVehicle{
				TripID:         fmt.Sprintf("SIM_TRK_%s_%s_%d", routeID, dir, idx),
				RouteID:        routeID,
				RouteShortName: rInfo.ShortName,
				RouteColor:     rInfo.Color,
				RouteTextColor: rInfo.TextColor,
				Direction:      dir,
				TrackType:      trackType,
				Placement:      placement,
				StopID:         currStop.StopID,
				FromStopID:     fromStopID,
				ToStopID:       toStopID,
				NodeIndex:      segIdx,
				StatusLabel:    statusLabel,
				Destination:    destNode.Name,
			})
		}
	}

	return vehicles
}
