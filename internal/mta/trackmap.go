package mta

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

// defaultExpressRoutes provides a fallback classification when all stations in a short sub-segment
// are shared by both local and express services.
var defaultExpressRoutes = map[string]bool{
	"A": true, "B": true, "D": true, "E": true, "N": true, "Q": true,
	"2": true, "3": true, "4": true, "5": true, "6X": true, "7X": true, "FX": true, "Z": true,
}

func (s *Service) normalizeRouteID(r string) string {
	switch r {
	case "SIR":
		return "SI"
	case "S":
		return "GS"
	default:
		return r
	}
}

// groupStationCorridors partitions a station's routes into distinct track corridors
// based on whether the routes share consecutive stops around the station.
// For example, at Jay St-MetroTech (A41: A, C, F), A & C share the Fulton/8th Av track corridor
// while F runs on the Culver/6th Av track corridor -> [["A", "C"], ["F"]].
// At Hoyt-Schermerhorn (A42: A, C, G) -> [["A", "C"], ["G"]].
func (s *Service) groupStationCorridors(st Station) [][]string {
	if len(st.Routes) <= 1 {
		if len(st.Routes) == 1 {
			return [][]string{{st.Routes[0]}}
		}
		return nil
	}

	type corrGroup struct {
		routes       []string
		neighborhood map[string]bool
	}
	var groups []corrGroup

	for _, r := range st.Routes {
		nr := s.normalizeRouteID(r)
		seq := s.static.DaytimeSequences[nr+"_S"]
		if !containsStr(seq, st.ID) {
			seq = s.static.CanonicalSequences[nr+"_S"]
		}
		if !containsStr(seq, st.ID) {
			if rev := s.static.CanonicalSequences[nr+"_N"]; containsStr(rev, st.ID) {
				seq = reverseCopy(rev)
			}
		}

		nb := make(map[string]bool)
		idx := indexOfStr(seq, st.ID)
		if idx != -1 {
			start := idx - 3
			if start < 0 {
				start = 0
			}
			end := idx + 3
			if end >= len(seq) {
				end = len(seq) - 1
			}
			for k := start; k <= end; k++ {
				nb[seq[k]] = true
			}
		} else {
			nb[st.ID] = true
		}

		placed := false
		for gi := range groups {
			overlap := 0
			for stopID := range nb {
				if groups[gi].neighborhood[stopID] {
					overlap++
				}
			}
			// Two routes share a physical corridor through this station if they share at least 2 stops in the immediate ±3 stop neighborhood
			if overlap >= 2 {
				groups[gi].routes = append(groups[gi].routes, r)
				for stopID := range nb {
					groups[gi].neighborhood[stopID] = true
				}
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, corrGroup{
				routes:       []string{r},
				neighborhood: nb,
			})
		}
	}

	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		sort.Strings(g.routes)
		out = append(out, g.routes)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i][0] < out[j][0]
	})
	return out
}

func (s *Service) corridorLineName(st Station, corr []string, multiCorridor bool) string {
	if !multiCorridor || len(corr) == 0 {
		return st.Line
	}
	switch corr[0] {
	case "F", "B", "D", "M":
		return "6th Av - Culver"
	case "G":
		return "Crosstown"
	case "A", "C", "E":
		return st.Line
	case "N", "Q", "R", "W":
		return "Broadway"
	case "J", "Z":
		return "Nassau St"
	case "L":
		return "Canarsie"
	case "1", "2", "3":
		return "Broadway - 7th Av"
	case "4", "5", "6":
		return "Lexington Av"
	case "7":
		return "Flushing"
	default:
		return st.Line
	}
}

// resolveCorridorAndTransfers determines:
// 1. Which corridor routes are currently active at `st` given `focusRoute` (e.g., ["A", "C"] vs ["F"] at Jay St).
// 2. All clickable TransferLinks at this station/complex (both other corridors on the same GTFS stop ID and sibling platforms in the same ComplexID).
func (s *Service) resolveCorridorAndTransfers(st Station, focusRoute string) (activeRoutes []string, transferLinks []TransferLink) {
	corridors := s.groupStationCorridors(st)
	if len(corridors) == 0 {
		activeRoutes = st.Routes
	} else {
		selectedIdx := 0
		if focusRoute != "" {
			for idx, corr := range corridors {
				if containsStr(corr, focusRoute) {
					selectedIdx = idx
					break
				}
			}
		}
		activeRoutes = corridors[selectedIdx]

		multi := len(corridors) > 1
		for idx, corr := range corridors {
			if len(corr) == 0 {
				continue
			}
			transferLinks = append(transferLinks, TransferLink{
				StationID:  st.ID,
				LineName:   s.corridorLineName(st, corr, multi),
				Routes:     corr,
				FocusRoute: corr[0],
				IsActive:   idx == selectedIdx,
			})
		}
	}

	// Also add sibling platforms from the same station ComplexID (e.g., R29 Broadway R at Jay St-MetroTech)
	if st.ComplexID != "" {
		siblings := s.complexMap[st.ComplexID]
		for _, sib := range siblings {
			if sib.ID == st.ID {
				continue
			}
			sibCorridors := s.groupStationCorridors(sib)
			multiSib := len(sibCorridors) > 1
			for _, corr := range sibCorridors {
				if len(corr) == 0 {
					continue
				}
				transferLinks = append(transferLinks, TransferLink{
					StationID:  sib.ID,
					LineName:   s.corridorLineName(sib, corr, multiSib),
					Routes:     corr,
					FocusRoute: corr[0],
					IsActive:   false,
				})
			}
		}
	}

	sort.Slice(transferLinks, func(i, j int) bool {
		if transferLinks[i].FocusRoute != transferLinks[j].FocusRoute {
			return transferLinks[i].FocusRoute < transferLinks[j].FocusRoute
		}
		return transferLinks[i].StationID < transferLinks[j].StationID
	})

	return activeRoutes, transferLinks
}

// buildCorridorSpine constructs a North-to-South ordered station sequence along the active corridor's routes,
// merging local and express stops so that both local-only and shared express stations appear in proper geographic order.
func (s *Service) buildCorridorSpine(st Station, corridorRoutes []string) []string {
	if len(corridorRoutes) == 0 {
		corridorRoutes = st.Routes
	}

	// Determine the primary stop ID prefix for the active corridor routes
	prefixCounts := map[string]int{}
	type candidateSeq struct {
		route      string
		stops      []string
		samePrefix int
	}

	var rawCandidates []candidateSeq
	routesToTry := append([]string{}, corridorRoutes...)
	if len(routesToTry) == 1 && (routesToTry[0] == "S" || routesToTry[0] == "SIR") {
		for _, alt := range []string{"GS", "FS", "H", "SI"} {
			routesToTry = append(routesToTry, alt)
		}
	}

	seenRoute := map[string]bool{}
	for _, rawRoute := range routesToTry {
		r := s.normalizeRouteID(rawRoute)
		if seenRoute[r] {
			continue
		}
		seenRoute[r] = true

		seq := s.static.CanonicalSequences[r+"_S"]
		if !containsStr(seq, st.ID) {
			if rev := s.static.CanonicalSequences[r+"_N"]; containsStr(rev, st.ID) {
				seq = reverseCopy(rev)
			}
		}
		if !containsStr(seq, st.ID) {
			continue
		}
		for _, sid := range seq {
			if len(sid) > 0 {
				prefixCounts[sid[:1]]++
			}
		}
		rawCandidates = append(rawCandidates, candidateSeq{
			route: r,
			stops: seq,
		})
	}

	if len(rawCandidates) == 0 {
		return []string{st.ID}
	}

	primaryPrefix := st.ID[:1]
	maxPCount := 0
	for p, cnt := range prefixCounts {
		if cnt > maxPCount {
			maxPCount = cnt
			primaryPrefix = p
		}
	}

	for i := range rawCandidates {
		sameP := 0
		for _, sid := range rawCandidates[i].stops {
			if strings.HasPrefix(sid, primaryPrefix) {
				sameP++
			}
		}
		rawCandidates[i].samePrefix = sameP
	}

	sort.Slice(rawCandidates, func(i, j int) bool {
		iMatchesTrunk := rawCandidates[i].samePrefix >= 3
		jMatchesTrunk := rawCandidates[j].samePrefix >= 3
		if iMatchesTrunk != jMatchesTrunk {
			return iMatchesTrunk
		}
		return len(rawCandidates[i].stops) > len(rawCandidates[j].stops)
	})

	spine := append([]string{}, rawCandidates[0].stops...)

	for _, cand := range rawCandidates[1:] {
		shared := 0
		for _, sid := range cand.stops {
			if containsStr(spine, sid) {
				shared++
			}
		}
		if shared < 2 {
			continue
		}
		for i, sid := range cand.stops {
			if containsStr(spine, sid) {
				continue
			}
			hasSharedAfter := false
			for f := i + 1; f < len(cand.stops); f++ {
				if containsStr(spine, cand.stops[f]) {
					hasSharedAfter = true
					break
				}
			}
			if !strings.HasPrefix(sid, primaryPrefix) && !hasSharedAfter {
				continue
			}
			predIdx := -1
			for p := i - 1; p >= 0; p-- {
				if idx := indexOfStr(spine, cand.stops[p]); idx != -1 {
					predIdx = idx
					break
				}
			}
			if predIdx != -1 {
				spine = append(spine[:predIdx+1], append([]string{sid}, spine[predIdx+1:]...)...)
			}
		}
	}

	return spine
}

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

	spine := s.buildCorridorSpine(st, corridorRoutes)
	targetIdx := indexOfStr(spine, st.ID)
	if targetIdx == -1 {
		targetIdx = 0
	}

	routes := corridorRoutes
	allowedRoutesSet := make(map[string]bool, len(routes))
	for _, r := range routes {
		allowedRoutesSet[r] = true
		allowedRoutesSet[s.normalizeRouteID(r)] = true
	}

	daytimeSet := make(map[string]map[string]bool)
	canonicalSet := make(map[string]map[string]bool)
	for _, r := range routes {
		nr := s.normalizeRouteID(r)
		dset := make(map[string]bool)
		for _, sid := range s.static.DaytimeSequences[nr+"_S"] {
			dset[sid] = true
		}
		for _, sid := range s.static.DaytimeSequences[nr+"_N"] {
			dset[sid] = true
		}
		cset := make(map[string]bool)
		for _, sid := range s.static.CanonicalSequences[nr+"_S"] {
			cset[sid] = true
		}
		for _, sid := range s.static.CanonicalSequences[nr+"_N"] {
			cset[sid] = true
		}
		if len(dset) == 0 {
			dset = cset
		}
		daytimeSet[r] = dset
		canonicalSet[r] = cset
	}

	skippedStopsOnSpine := make(map[string]int)
	for _, r := range routes {
		firstIdx, lastIdx := -1, -1
		stoppedCount := 0
		for idx, sid := range spine {
			if daytimeSet[r][sid] {
				if firstIdx == -1 {
					firstIdx = idx
				}
				lastIdx = idx
				stoppedCount++
			}
		}
		if firstIdx != -1 && lastIdx > firstIdx {
			spanLen := lastIdx - firstIdx + 1
			skippedStopsOnSpine[r] = spanLen - stoppedCount
		}
	}

	var localRoutes, expressRoutes []string
	if len(routes) == 1 {
		localRoutes = append(localRoutes, routes[0])
	} else {
		for _, r := range routes {
			if skippedStopsOnSpine[r] >= 2 {
				expressRoutes = append(expressRoutes, r)
			} else {
				localRoutes = append(localRoutes, r)
			}
		}
		if len(expressRoutes) == 0 && len(routes) > 1 {
			for _, r := range routes {
				if defaultExpressRoutes[r] && !containsStr(expressRoutes, r) {
					expressRoutes = append(expressRoutes, r)
				}
			}
			var filteredLocal []string
			for _, r := range localRoutes {
				if !containsStr(expressRoutes, r) {
					filteredLocal = append(filteredLocal, r)
				}
			}
			localRoutes = filteredLocal
		}
		if len(localRoutes) == 0 && len(expressRoutes) > 0 {
			localRoutes = expressRoutes
			expressRoutes = nil
		}
	}

	hasExpress := len(expressRoutes) > 0
	isExpressRoute := make(map[string]bool)
	for _, r := range expressRoutes {
		isExpressRoute[r] = true
	}

	nodes := make([]TrackDiagramNode, 0, len(spine))
	spineIdxMap := make(map[string]int, len(spine))
	for i, sid := range spine {
		spineIdxMap[sid] = i
		stInfo := s.stationMap[sid]
		name := s.stopNames[sid]
		if name == "" {
			name = sid
		}

		var nodeLocal, nodeExpress, nodeAll []string
		for _, r := range localRoutes {
			if daytimeSet[r][sid] || canonicalSet[r][sid] {
				nodeLocal = append(nodeLocal, r)
				nodeAll = append(nodeAll, r)
			}
		}
		for _, r := range expressRoutes {
			if daytimeSet[r][sid] {
				nodeExpress = append(nodeExpress, r)
				nodeAll = append(nodeAll, r)
			}
		}
		if len(nodeLocal) == 0 && len(nodeExpress) == 0 {
			for _, r := range stInfo.Routes {
				if !allowedRoutesSet[r] {
					continue
				}
				if isExpressRoute[r] && daytimeSet[r][sid] {
					nodeExpress = append(nodeExpress, r)
				} else {
					nodeLocal = append(nodeLocal, r)
				}
				nodeAll = append(nodeAll, r)
			}
		}

		// Compute other routes/lines serving this station or its complex (not in the current corridor)
		_, allNodeTransfers := s.resolveCorridorAndTransfers(stInfo, "")
		var otherLinks []TransferLink
		for _, tl := range allNodeTransfers {
			var nonCorridorRoutes []string
			for _, trRoute := range tl.Routes {
				if !allowedRoutesSet[trRoute] {
					nonCorridorRoutes = append(nonCorridorRoutes, trRoute)
				}
			}
			if len(nonCorridorRoutes) > 0 {
				otherLinks = append(otherLinks, TransferLink{
					StationID:  tl.StationID,
					LineName:   tl.LineName,
					Routes:     nonCorridorRoutes,
					FocusRoute: nonCorridorRoutes[0],
					IsActive:   false,
				})
			}
		}

		nodes = append(nodes, TrackDiagramNode{
			StopID:          sid,
			Name:            name,
			Borough:         stInfo.Borough,
			IsTarget:        sid == st.ID,
			OffsetFromFocus: i - targetIdx,
			LocalRoutes:     nodeLocal,
			ExpressRoutes:   nodeExpress,
			AllStopRoutes:   nodeAll,
			OtherLinks:      otherLinks,
		})
	}

	n := len(nodes)
	bypassIndices := []int{}
	for i := 0; i < n; i++ {
		bypassHere := hasExpress && len(nodes[i].ExpressRoutes) == 0 && len(nodes[i].LocalRoutes) > 0
		nodes[i].HasExpressBypass = bypassHere
		if bypassHere {
			bypassIndices = append(bypassIndices, i)
		}
	}

	segTypes := make([]string, n)
	for i := 0; i < n; i++ {
		segTypes[i] = "MERGED"
	}

	if hasExpress && len(bypassIndices) > 0 {
		for _, bIdx := range bypassIndices {
			if bIdx > 0 {
				segTypes[bIdx-1] = "SPLIT"
			}
			if bIdx < n-1 {
				segTypes[bIdx] = "SPLIT"
			}
		}
		for k := 0; k+1 < len(bypassIndices); k++ {
			b1 := bypassIndices[k]
			b2 := bypassIndices[k+1]
			if b2-b1 <= 3 {
				for sIdx := b1; sIdx < b2; sIdx++ {
					segTypes[sIdx] = "SPLIT"
				}
			}
		}
	}

	for i := 0; i < n; i++ {
		nodes[i].NextSegmentTracks = segTypes[i]
		prevSeg := segTypes[i]
		if i > 0 {
			prevSeg = segTypes[i-1]
		}
		nextSeg := segTypes[i]

		if !hasExpress {
			nodes[i].IsMergedTrack = true
			nodes[i].IsExpressStation = false
		} else if nodes[i].HasExpressBypass {
			nodes[i].IsMergedTrack = false
			nodes[i].IsExpressStation = false
		} else if prevSeg == "SPLIT" && nextSeg == "SPLIT" {
			nodes[i].IsMergedTrack = false
			nodes[i].IsExpressStation = true
		} else if prevSeg == "MERGED" && nextSeg == "MERGED" {
			nodes[i].IsMergedTrack = true
			nodes[i].IsExpressStation = false
		} else {
			nodes[i].IsMergedTrack = false
			nodes[i].IsExpressStation = true
		}
	}

	var vehicles []TrackVehicle
	if forceSim || len(validFeeds) == 0 {
		vehicles = s.buildSimulatedTrackVehicles(st, nodes, spineIdxMap, localRoutes, expressRoutes, hasExpress)
	} else {
		vehicles = s.extractLiveTrackVehicles(nodes, spineIdxMap, allowedRoutesSet, isExpressRoute, hasExpress, validFeeds)
	}

	northLabel := st.NorthLabel
	if northLabel == "" {
		northLabel = "Northbound"
	}
	southLabel := st.SouthLabel
	if southLabel == "" {
		southLabel = "Southbound"
	}

	return TrackMapData{
		LineName:        s.corridorLineName(st, routes, true),
		NorthLabel:      northLabel,
		SouthLabel:      southLabel,
		CorridorRoutes:  routes,
		LocalRoutes:     localRoutes,
		ExpressRoutes:   expressRoutes,
		HasExpressTrack: hasExpress,
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

			trackType := ClassifyTrackType(reportedTrack)

			if hasExpress {
				// If NYCT extension track is not present or not classified, infer dynamically
				if trackType == "" {
					currIsBypass := nodes[nodeIdx].HasExpressBypass
					nextIsBypass := false
					diff := 0
					if currIdx+1 < len(stus) {
						nextClean := CleanStopID(stus[currIdx+1].GetStopId())
						if nextSpineIdx, ok := spineIdxMap[nextClean]; ok {
							nextIsBypass = nodes[nextSpineIdx].HasExpressBypass
							diff = nextSpineIdx - nodeIdx
							if diff < 0 {
								diff = -diff
							}
						}
					}

					if currIsBypass || nextIsBypass {
						trackType = "LOCAL"
					} else if diff > 1 {
						trackType = "EXPRESS"
					} else if diff == 1 {
						trackType = "LOCAL"
					} else if isExpressRoute[routeID] || defaultExpressRoutes[routeID] {
						trackType = "EXPRESS"
					} else {
						trackType = "LOCAL"
					}
				}
			} else {
				trackType = "LOCAL"
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

func containsStr(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func indexOfStr(slice []string, target string) int {
	for i, s := range slice {
		if s == target {
			return i
		}
	}
	return -1
}

func reverseCopy(slice []string) []string {
	out := make([]string, len(slice))
	for i := range slice {
		out[i] = slice[len(slice)-1-i]
	}
	return out
}
