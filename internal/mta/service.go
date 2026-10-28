package mta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

var allFeeds = []string{
	"gtfs-ace",
	"gtfs-bdfm",
	"gtfs-g",
	"gtfs-jz",
	"gtfs-nqrw",
	"gtfs-l",
	"gtfs",
	"gtfs-si",
}

var routeToFeed = map[string]string{
	"A": "gtfs-ace", "C": "gtfs-ace", "E": "gtfs-ace", "H": "gtfs-ace", "FS": "gtfs-ace", "SR": "gtfs-ace",
	"B": "gtfs-bdfm", "D": "gtfs-bdfm", "F": "gtfs-bdfm", "FX": "gtfs-bdfm", "M": "gtfs-bdfm",
	"G": "gtfs-g",
	"J": "gtfs-jz", "Z": "gtfs-jz",
	"N": "gtfs-nqrw", "Q": "gtfs-nqrw", "R": "gtfs-nqrw", "W": "gtfs-nqrw",
	"L": "gtfs-l",
	"1": "gtfs", "2": "gtfs", "3": "gtfs", "4": "gtfs", "5": "gtfs", "6": "gtfs", "6X": "gtfs",
	"7": "gtfs", "7X": "gtfs", "GS": "gtfs", "S": "gtfs",
	"SI": "gtfs-si", "SIR": "gtfs-si",
}

type cachedFeed struct {
	msg       *gtfs.FeedMessage
	fetchedAt time.Time
}

// Service manages MTA static metadata and live GTFS-RT feeds with fallback simulation.
type Service struct {
	static     StaticData
	stationMap map[string]Station
	complexMap map[string][]Station
	stopNames  map[string]string
	httpClient *http.Client

	mu        sync.RWMutex
	feedCache map[string]cachedFeed
}

// NewService loads the static MTA station/route JSON and initializes the GTFS-RT service.
func NewService(staticJSONPath string) (*Service, error) {
	b, err := os.ReadFile(staticJSONPath)
	if err != nil {
		return nil, fmt.Errorf("reading static mta data: %w", err)
	}
	var static StaticData
	if err := json.Unmarshal(b, &static); err != nil {
		return nil, fmt.Errorf("unmarshaling static mta data: %w", err)
	}

	stationMap := make(map[string]Station, len(static.Stations))
	complexMap := make(map[string][]Station)
	stopNames := make(map[string]string, len(static.Stations)+8)
	for _, s := range static.Stations {
		stationMap[s.ID] = s
		stopNames[s.ID] = s.Name
		if s.ComplexID != "" {
			complexMap[s.ComplexID] = append(complexMap[s.ComplexID], s)
		}
	}
	// Special non-revenue / yard / relay stop names occasionally seen in MTA GTFS-RT feeds
	stopNames["A39"] = "Hoyt-Schermerhorn Relay"
	stopNames["D23"] = "DeKalb Av Junction"

	return &Service{
		static:     static,
		stationMap: stationMap,
		complexMap: complexMap,
		stopNames:  stopNames,
		httpClient: &http.Client{Timeout: 6 * time.Second},
		feedCache:  make(map[string]cachedFeed),
	}, nil
}

// GetStations returns all stations in the system along with route metadata.
func (s *Service) GetStations() ([]Station, map[string]RouteInfo) {
	return s.static.Stations, s.static.Routes
}

// GetStaticData returns the full static dataset including sequence topology.
func (s *Service) GetStaticData() StaticData {
	return s.static
}

// GetStation returns a single station by ID.
func (s *Service) GetStation(id string) (Station, bool) {
	st, ok := s.stationMap[id]
	return st, ok
}

// CleanStopID strips directional 'N' or 'S' suffixes from a GTFS stop ID.
func CleanStopID(id string) string {
	return strings.TrimSuffix(strings.TrimSuffix(id, "N"), "S")
}

func (s *Service) getRouteInfo(routeID string) RouteInfo {
	if r, ok := s.static.Routes[routeID]; ok {
		return r
	}
	// Fallback for S /SIR variations
	if routeID == "S" {
		if r, ok := s.static.Routes["GS"]; ok {
			return r
		}
	}
	return RouteInfo{
		ID:        routeID,
		ShortName: routeID,
		LongName:  routeID + " Train",
		Color:     "#7C858C",
		TextColor: "#FFFFFF",
	}
}

// feedsForStation returns the prioritized list of MTA GTFS-RT feeds to query for a station.
// Since trains are sometimes rerouted over different lines (e.g., F via A/C or N/Q via R),
// we fetch the station's primary feeds plus any overlapping division feeds.
func (s *Service) feedsForStation(st Station) []string {
	seen := map[string]bool{}
	var feeds []string
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			feeds = append(feeds, f)
		}
	}
	for _, r := range st.Routes {
		add(routeToFeed[r])
		if r == "S" {
			add("gtfs")
			add("gtfs-ace")
		}
	}
	// Include companion B-Division feeds for cross-honored / rerouted trains
	if seen["gtfs-ace"] || seen["gtfs-bdfm"] || seen["gtfs-nqrw"] || seen["gtfs-g"] {
		add("gtfs-ace")
		add("gtfs-bdfm")
		add("gtfs-nqrw")
		add("gtfs-g")
	}
	if len(feeds) == 0 {
		return allFeeds
	}
	return feeds
}

func (s *Service) fetchFeed(ctx context.Context, feedName string) (*gtfs.FeedMessage, error) {
	s.mu.RLock()
	cached, ok := s.feedCache[feedName]
	s.mu.RUnlock()
	if ok && time.Since(cached.fetchedAt) < 15*time.Second && cached.msg != nil {
		return cached.msg, nil
	}

	url := fmt.Sprintf("https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%%2F%s", feedName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/x-protobuf")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if ok && cached.msg != nil {
			return cached.msg, nil
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if ok && cached.msg != nil {
			return cached.msg, nil
		}
		return nil, fmt.Errorf("mta feed %s returned status %d", feedName, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var msg gtfs.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.feedCache[feedName] = cachedFeed{
		msg:       &msg,
		fetchedAt: time.Now(),
	}
	s.mu.Unlock()

	return &msg, nil
}

// GetStationBoard fetches live GTFS-RT feeds (or falls back to simulation if offline or requested)
// and builds the location-based countdown clock and track map for a given station and optional focusRoute.
func (s *Service) GetStationBoard(ctx context.Context, stationID string, focusRoute string, forceSimulate bool) (*StationBoardResponse, error) {
	st, ok := s.stationMap[stationID]
	if !ok {
		return nil, fmt.Errorf("station %q not found", stationID)
	}

	activeRoutes, transferLinks := s.resolveCorridorAndTransfers(st, focusRoute)
	trunkRoutes := TrunkCorridorRoutes(activeRoutes)
	allowedRoutesSet := make(map[string]bool, len(trunkRoutes))
	for _, r := range trunkRoutes {
		allowedRoutesSet[r] = true
		allowedRoutesSet[s.normalizeRouteID(r)] = true
	}

	northLabel := st.NorthLabel
	if northLabel == "" {
		northLabel = "Northbound / Terminal"
	}
	southLabel := st.SouthLabel
	if southLabel == "" {
		southLabel = "Southbound / Terminal"
	}

	resp := &StationBoardResponse{
		Station:       st,
		ActiveRoute:   focusRoute,
		ActiveRoutes:  activeRoutes,
		TransferLinks: transferLinks,
		RouteMetadata: s.static.Routes,
		Northbound: DirectionBoard{
			Direction: "N",
			Label:     northLabel,
			Trains:    []TrainArrival{},
		},
		Southbound: DirectionBoard{
			Direction: "S",
			Label:     southLabel,
			Trains:    []TrainArrival{},
		},
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}

	if forceSimulate {
		s.populateSimulatedTrains(resp)
		resp.TrackMap = s.buildTrackMap(st, trunkRoutes, nil, true)
		return resp, nil
	}

	feedsToQuery := s.feedsForStation(st)
	type feedResult struct {
		msg *gtfs.FeedMessage
		err error
	}
	results := make([]feedResult, len(feedsToQuery))
	var wg sync.WaitGroup
	for i, fn := range feedsToQuery {
		wg.Add(1)
		go func(idx int, feedName string) {
			defer wg.Done()
			msg, err := s.fetchFeed(ctx, feedName)
			results[idx] = feedResult{msg: msg, err: err}
		}(i, fn)
	}
	wg.Wait()

	var validFeeds []*gtfs.FeedMessage
	var latestTS int64
	for _, r := range results {
		if r.msg != nil {
			validFeeds = append(validFeeds, r.msg)
			ts := int64(r.msg.GetHeader().GetTimestamp())
			if ts > latestTS {
				latestTS = ts
			}
		}
	}

	if len(validFeeds) == 0 {
		// Automatic fallback to simulated mode when offline
		s.populateSimulatedTrains(resp)
		resp.TrackMap = s.buildTrackMap(st, trunkRoutes, nil, true)
		return resp, nil
	}

	resp.DataSource = "LIVE_MTA_GTFS_RT"
	resp.FeedTimestamp = latestTS

	targetN := st.ID + "N"
	targetS := st.ID + "S"

	northTrains := []TrainArrival{}
	southTrains := []TrainArrival{}
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
			// Filter to only the active corridor routes for this station/line view
			if len(allowedRoutesSet) > 0 && !allowedRoutesSet[routeID] && !allowedRoutesSet[s.normalizeRouteID(routeID)] {
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

			for i, stu := range stus {
				stopID := stu.GetStopId()
				if stopID != targetN && stopID != targetS {
					continue
				}
				dir := "N"
				if stopID == targetS {
					dir = "S"
				}

				arr, ok := s.resolveTrainArrival(st, dir, i, tu, vehMap[tripID], feedTS)
				if !ok {
					continue
				}
				seenTrips[tripID] = true
				if dir == "N" {
					northTrains = append(northTrains, arr)
				} else {
					southTrains = append(southTrains, arr)
				}
				break
			}
		}
	}

	sort.Slice(northTrains, func(i, j int) bool {
		if northTrains[i].StopsAway != northTrains[j].StopsAway {
			return northTrains[i].StopsAway < northTrains[j].StopsAway
		}
		return northTrains[i].SortOrder < northTrains[j].SortOrder
	})
	sort.Slice(southTrains, func(i, j int) bool {
		if southTrains[i].StopsAway != southTrains[j].StopsAway {
			return southTrains[i].StopsAway < southTrains[j].StopsAway
		}
		return southTrains[i].SortOrder < southTrains[j].SortOrder
	})

	if len(northTrains) > 8 {
		northTrains = northTrains[:8]
	}
	if len(southTrains) > 8 {
		southTrains = southTrains[:8]
	}

	resp.Northbound.Trains = northTrains
	resp.Southbound.Trains = southTrains

	if len(northTrains) == 0 && st.NorthLabel != "" {
		resp.Northbound.ServiceNote = "No active trains currently scheduled for this platform in live MTA feed (possible reroute or off-hours service change)."
	}
	if len(southTrains) == 0 && st.SouthLabel != "" {
		resp.Southbound.ServiceNote = "No active trains currently scheduled for this platform in live MTA feed (possible reroute or off-hours service change)."
	}

	resp.TrackMap = s.buildTrackMap(st, trunkRoutes, validFeeds, false)

	return resp, nil
}

func (s *Service) resolveTrainArrival(
	targetStation Station,
	dir string,
	targetIdx int,
	tu *gtfs.TripUpdate,
	veh *gtfs.VehiclePosition,
	feedTS int64,
) (TrainArrival, bool) {
	stus := tu.GetStopTimeUpdate()
	targetSTU := stus[targetIdx]
	targetTime := targetSTU.GetArrival().GetTime()
	if targetTime == 0 {
		targetTime = targetSTU.GetDeparture().GetTime()
	}
	// Ignore trains that already left the target station more than 45 seconds ago
	if targetTime > 0 && targetTime < feedTS-45 {
		return TrainArrival{}, false
	}

	// Determine the train's current stop index in stus (first stop not yet departed)
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
	if currIdx == -1 || currIdx > targetIdx {
		return TrainArrival{}, false
	}

	// Ignore unstarted future trips still sitting at the origin terminal > 22 minutes before departure
	if currIdx == 0 && targetIdx > 0 {
		firstTime := stus[0].GetDeparture().GetTime()
		if firstTime == 0 {
			firstTime = stus[0].GetArrival().GetTime()
		}
		if firstTime-feedTS > 900 {
			return TrainArrival{}, false
		}
	}

	stopsAway := targetIdx - currIdx
	currStopID := CleanStopID(stus[currIdx].GetStopId())
	currStopName := s.stopNames[currStopID]
	if currStopName == "" {
		currStopName = currStopID
	}

	termStopID := CleanStopID(stus[len(stus)-1].GetStopId())
	destName := s.stopNames[termStopID]
	if destName == "" {
		destName = termStopID
	}

	routeID := tu.GetTrip().GetRouteId()
	rInfo := s.getRouteInfo(routeID)

	// Find previous stop ID and name from STUs, VehiclePosition, or canonical stop sequence
	prevStopID := ""
	prevStopName := ""
	if currIdx > 0 {
		prevID := CleanStopID(stus[currIdx-1].GetStopId())
		if s.stopNames[prevID] != "" {
			prevStopID = prevID
			prevStopName = s.stopNames[prevID]
		}
	}
	if prevStopName == "" && veh != nil {
		vpStopID := CleanStopID(veh.GetStopId())
		if vpStopID != "" && vpStopID != currStopID && s.stopNames[vpStopID] != "" {
			prevStopID = vpStopID
			prevStopName = s.stopNames[vpStopID]
		}
	}
	if prevStopName == "" {
		seq := s.static.CanonicalSequences[routeID+"_"+dir]
		for idx, sid := range seq {
			if sid == currStopID && idx > 0 {
				prevStopID = seq[idx-1]
				prevStopName = s.stopNames[prevStopID]
				break
			}
		}
	}

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
	var locationStatus, statusType string

	if stopsAway == 0 {
		if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_STOPPED_AT) || (arrDiff <= 15 && depDiff >= -30) {
			locationStatus = fmt.Sprintf("At %s (Boarding in Station)", targetStation.Name)
			statusType = "AT_TARGET"
		} else if arrDiff <= 50 {
			locationStatus = fmt.Sprintf("Approaching %s", targetStation.Name)
			statusType = "APPROACHING"
		} else if prevStopName != "" && prevStopName != targetStation.Name {
			locationStatus = fmt.Sprintf("Between %s & %s", prevStopName, targetStation.Name)
			statusType = "BETWEEN"
		} else {
			locationStatus = fmt.Sprintf("Approaching %s", targetStation.Name)
			statusType = "APPROACHING"
		}
	} else {
		if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_STOPPED_AT) || (arrDiff <= 15 && depDiff >= -25) {
			locationStatus = fmt.Sprintf("At %s", currStopName)
			statusType = "AT_STATION"
		} else if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_INCOMING_AT) || (arrDiff > 15 && arrDiff <= 45) {
			locationStatus = fmt.Sprintf("Approaching %s", currStopName)
			statusType = "APPROACHING"
		} else if prevStopName != "" && prevStopName != currStopName {
			locationStatus = fmt.Sprintf("Between %s & %s", prevStopName, currStopName)
			statusType = "BETWEEN"
		} else {
			locationStatus = fmt.Sprintf("Approaching %s", currStopName)
			statusType = "APPROACHING"
		}
	}

	var stopsAwayLabel string
	switch stopsAway {
	case 0:
		if statusType == "AT_TARGET" {
			stopsAwayLabel = "In Station"
		} else {
			stopsAwayLabel = "Entering Station"
		}
	case 1:
		stopsAwayLabel = "1 stop away"
	default:
		stopsAwayLabel = fmt.Sprintf("%d stops away", stopsAway)
	}

	// Build a recent path snippet (from current stop up to target station, max 6 stops) for UI strip diagram
	var recentPath []string
	var recentStopIDs []string
	startIdx := currIdx
	if targetIdx-startIdx > 4 {
		startIdx = targetIdx - 4
	}
	if currIdx < startIdx {
		recentPath = append(recentPath, currStopName, "...")
		recentStopIDs = append(recentStopIDs, currStopID, "")
	}
	for k := startIdx; k <= targetIdx; k++ {
		sid := CleanStopID(stus[k].GetStopId())
		sname := s.stopNames[sid]
		if sname != "" {
			recentPath = append(recentPath, sname)
			recentStopIDs = append(recentStopIDs, sid)
		}
	}

	targetTrackInfo := ExtractNyctTrackInfo(targetSTU)
	currTrackInfo := ExtractNyctTrackInfo(stus[currIdx])
	arrTrack := targetTrackInfo.ActualTrack
	if arrTrack == "" {
		arrTrack = currTrackInfo.ActualTrack
	}
	if arrTrack == "" {
		arrTrack = targetTrackInfo.ScheduledTrack
	}
	if arrTrack == "" {
		arrTrack = currTrackInfo.ScheduledTrack
	}

	return TrainArrival{
		TripID:            tu.GetTrip().GetTripId(),
		RouteID:           routeID,
		RouteShortName:    rInfo.ShortName,
		RouteColor:        rInfo.Color,
		RouteTextColor:    rInfo.TextColor,
		Direction:         dir,
		Destination:       destName,
		DestinationStopID: termStopID,
		LocationStatus:    locationStatus,
		StatusType:        statusType,
		CurrentStopID:     currStopID,
		CurrentStop:       currStopName,
		PreviousStopID:    prevStopID,
		PreviousStop:      prevStopName,
		StopsAway:         stopsAway,
		StopsAwayLabel:    stopsAwayLabel,
		Track:             arrTrack,
		RecentPath:        recentPath,
		RecentStopIDs:     recentStopIDs,
		SortOrder:         targetTime,
	}, true
}

// populateSimulatedTrains synthesizes realistic train positions along the station's canonical GTFS sequences
// when offline or when the user toggles Simulation Mode.
func (s *Service) populateSimulatedTrains(resp *StationBoardResponse) {
	resp.DataSource = "SIMULATED_FALLBACK"
	resp.FeedTimestamp = time.Now().Unix()

	st := resp.Station
	routes := resp.ActiveRoutes
	if len(routes) == 0 {
		routes = st.Routes
	}
	if len(routes) == 0 {
		routes = []string{"A", "C"}
	}

	// Deterministic time-stepped seed so simulated trains advance smoothly every 20 seconds
	timeBucket := time.Now().Unix() / 20

	buildDir := func(dir string) []TrainArrival {
		var arrivals []TrainArrival
		offsets := []int{0, 2, 4, 7, 11, 15}
		for i, baseOffset := range offsets {
			routeID := routes[i%len(routes)]
			rInfo := s.getRouteInfo(routeID)
			seq := s.static.CanonicalSequences[routeID+"_"+dir]
			if len(seq) == 0 {
				// Try alternate shuttle route IDs
				for _, alt := range []string{"GS", "FS", "H", "SI"} {
					if len(s.static.CanonicalSequences[alt+"_"+dir]) > 0 {
						seq = s.static.CanonicalSequences[alt+"_"+dir]
						break
					}
				}
			}
			if len(seq) == 0 {
				continue
			}

			targetIdx := -1
			for idx, sid := range seq {
				if sid == st.ID {
					targetIdx = idx
					break
				}
			}
			if targetIdx <= 0 {
				continue
			}

			// Animate train position based on timeBucket
			shift := int((timeBucket + int64(i)) % 2)
			stopsAway := baseOffset - shift
			if stopsAway < 0 {
				stopsAway = 0
			}
			currIdx := targetIdx - stopsAway
			if currIdx < 0 {
				currIdx = 0
				stopsAway = targetIdx
			}

			currStopID := seq[currIdx]
			currStopName := s.stopNames[currStopID]
			prevStopID := ""
			prevStopName := ""
			if currIdx > 0 {
				prevStopID = seq[currIdx-1]
				prevStopName = s.stopNames[prevStopID]
			}
			destStopID := seq[len(seq)-1]
			destName := s.stopNames[destStopID]

			rng := rand.New(rand.NewSource(timeBucket + int64(i*31) + int64(dir[0])))
			phase := rng.Intn(3)

			var locationStatus, statusType, stopsAwayLabel string
			if stopsAway == 0 {
				if phase == 0 {
					locationStatus = fmt.Sprintf("At %s (Boarding in Station)", st.Name)
					statusType = "AT_TARGET"
					stopsAwayLabel = "In Station"
				} else {
					locationStatus = fmt.Sprintf("Approaching %s", st.Name)
					statusType = "APPROACHING"
					stopsAwayLabel = "Entering Station"
				}
			} else {
				if phase == 0 || prevStopName == "" {
					locationStatus = fmt.Sprintf("At %s", currStopName)
					statusType = "AT_STATION"
				} else if phase == 1 {
					locationStatus = fmt.Sprintf("Between %s & %s", prevStopName, currStopName)
					statusType = "BETWEEN"
				} else {
					locationStatus = fmt.Sprintf("Approaching %s", currStopName)
					statusType = "APPROACHING"
				}
				if stopsAway == 1 {
					stopsAwayLabel = "1 stop away"
				} else {
					stopsAwayLabel = fmt.Sprintf("%d stops away", stopsAway)
				}
			}

			var recentPath []string
			var recentStopIDs []string
			startIdx := currIdx
			if targetIdx-startIdx > 4 {
				startIdx = targetIdx - 4
			}
			if currIdx < startIdx {
				recentPath = append(recentPath, currStopName, "...")
				recentStopIDs = append(recentStopIDs, currStopID, "")
			}
			for k := startIdx; k <= targetIdx; k++ {
				recentPath = append(recentPath, s.stopNames[seq[k]])
				recentStopIDs = append(recentStopIDs, seq[k])
			}

			arrivals = append(arrivals, TrainArrival{
				TripID:            fmt.Sprintf("SIM_%s_%s_%d", routeID, dir, i),
				RouteID:           routeID,
				RouteShortName:    rInfo.ShortName,
				RouteColor:        rInfo.Color,
				RouteTextColor:    rInfo.TextColor,
				Direction:         dir,
				Destination:       destName,
				DestinationStopID: destStopID,
				LocationStatus:    locationStatus,
				StatusType:        statusType,
				CurrentStopID:     currStopID,
				CurrentStop:       currStopName,
				PreviousStopID:    prevStopID,
				PreviousStop:      prevStopName,
				StopsAway:         stopsAway,
				StopsAwayLabel:    stopsAwayLabel,
				RecentPath:        recentPath,
				RecentStopIDs:     recentStopIDs,
			})
		}
		return arrivals
	}

	if st.NorthLabel != "" {
		resp.Northbound.Trains = buildDir("N")
	}
	if st.SouthLabel != "" {
		resp.Southbound.Trains = buildDir("S")
	}
}

// GetAllTrains fetches all 8 GTFS-RT feeds and returns a system-wide snapshot of all active trains.
func (s *Service) GetAllTrains(ctx context.Context) (*SystemTrainsResponse, error) {
	type feedResult struct {
		msg *gtfs.FeedMessage
		err error
	}
	results := make([]feedResult, len(allFeeds))
	var wg sync.WaitGroup
	for i, fn := range allFeeds {
		wg.Add(1)
		go func(idx int, feedName string) {
			defer wg.Done()
			msg, err := s.fetchFeed(ctx, feedName)
			results[idx] = feedResult{msg: msg, err: err}
		}(i, fn)
	}
	wg.Wait()

	var validFeeds []*gtfs.FeedMessage
	var latestTS int64
	for _, r := range results {
		if r.msg != nil {
			validFeeds = append(validFeeds, r.msg)
			ts := int64(r.msg.GetHeader().GetTimestamp())
			if ts > latestTS {
				latestTS = ts
			}
		}
	}

	resp := &SystemTrainsResponse{
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
		Trains:      []SystemTrain{},
	}

	if len(validFeeds) == 0 {
		resp.DataSource = "SIMULATED_FALLBACK"
		resp.FeedTimestamp = time.Now().Unix()
		resp.Trains = s.generateSimulatedSystemTrains()
		return resp, nil
	}

	resp.DataSource = "LIVE_MTA_GTFS_RT"
	resp.FeedTimestamp = latestTS
	if resp.FeedTimestamp == 0 {
		resp.FeedTimestamp = time.Now().Unix()
	}

	seenTrips := make(map[string]bool)
	for _, feed := range validFeeds {
		feedTS := int64(feed.GetHeader().GetTimestamp())
		if feedTS == 0 {
			feedTS = time.Now().Unix()
		}

		for _, entity := range feed.GetEntity() {
			tu := entity.GetTripUpdate()
			if tu == nil {
				continue
			}
			tripID := tu.GetTrip().GetTripId()
			if tripID == "" || seenTrips[tripID] {
				continue
			}
			routeID := tu.GetTrip().GetRouteId()
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

			currStopRaw := stus[currIdx].GetStopId()
			currStopID := CleanStopID(currStopRaw)
			dir := "S"
			if strings.HasSuffix(currStopRaw, "N") {
				dir = "N"
			}

			termStopID := CleanStopID(stus[len(stus)-1].GetStopId())
			destName := s.stopNames[termStopID]
			if destName == "" {
				destName = termStopID
			}

			currStopName := s.stopNames[currStopID]
			if currStopName == "" {
				currStopName = currStopID
			}

			rInfo := s.getRouteInfo(routeID)

			prevStopID := ""
			prevStopName := ""
			if currIdx > 0 {
				prevID := CleanStopID(stus[currIdx-1].GetStopId())
				if s.stopNames[prevID] != "" {
					prevStopID = prevID
					prevStopName = s.stopNames[prevID]
				}
			}

			var upcoming []string
			for k := currIdx; k < len(stus); k++ {
				sid := CleanStopID(stus[k].GetStopId())
				if sid != "" {
					upcoming = append(upcoming, sid)
				}
			}

			statusType := "APPROACHING"
			arrTime := stus[currIdx].GetArrival().GetTime()
			if arrTime == 0 {
				arrTime = stus[currIdx].GetDeparture().GetTime()
			}
			if arrTime > 0 && (arrTime-feedTS) <= 15 && (feedTS-arrTime) <= 45 {
				statusType = "AT_STATION"
			} else if prevStopID != "" && (arrTime-feedTS) > 40 {
				statusType = "BETWEEN"
			}

			seenTrips[tripID] = true
			resp.Trains = append(resp.Trains, SystemTrain{
				TripID:            tripID,
				RouteID:           routeID,
				RouteShortName:    rInfo.ShortName,
				RouteColor:        rInfo.Color,
				RouteTextColor:    rInfo.TextColor,
				Direction:         dir,
				Destination:       destName,
				DestinationStopID: termStopID,
				StatusType:        statusType,
				CurrentStopID:     currStopID,
				CurrentStop:       currStopName,
				PreviousStopID:    prevStopID,
				PreviousStop:      prevStopName,
				UpcomingStopIDs:   upcoming,
			})
		}
	}

	return resp, nil
}

func (s *Service) generateSimulatedSystemTrains() []SystemTrain {
	var trains []SystemTrain
	timeBucket := time.Now().Unix() / 20
	routes := []string{"1", "2", "3", "4", "5", "6", "7", "A", "C", "E", "B", "D", "F", "M", "G", "J", "L", "N", "Q", "R"}

	for _, r := range routes {
		for _, dir := range []string{"N", "S"} {
			seq := s.static.CanonicalSequences[r+"_"+dir]
			if len(seq) < 4 {
				continue
			}
			rInfo := s.getRouteInfo(r)
			destStopID := seq[len(seq)-1]
			destName := s.stopNames[destStopID]
			if destName == "" {
				destName = destStopID
			}

			// Place 2-3 trains per route
			for idx := 0; idx < 3; idx++ {
				offset := int((timeBucket*2 + int64(idx*7) + int64(len(r))) % int64(len(seq)-1))
				currStopID := seq[offset]
				currStopName := s.stopNames[currStopID]
				if currStopName == "" {
					currStopName = currStopID
				}
				prevStopID := ""
				prevStopName := ""
				if offset > 0 {
					prevStopID = seq[offset-1]
					prevStopName = s.stopNames[prevStopID]
				}
				var upcoming []string
				for k := offset; k < len(seq); k++ {
					upcoming = append(upcoming, seq[k])
				}

				statusType := "APPROACHING"
				if (timeBucket+int64(idx))%2 == 0 {
					statusType = "AT_STATION"
				}

				trains = append(trains, SystemTrain{
					TripID:            fmt.Sprintf("SIM_%s_%s_%d", r, dir, idx),
					RouteID:           r,
					RouteShortName:    rInfo.ShortName,
					RouteColor:        rInfo.Color,
					RouteTextColor:    rInfo.TextColor,
					Direction:         dir,
					Destination:       destName,
					DestinationStopID: destStopID,
					StatusType:        statusType,
					CurrentStopID:     currStopID,
					CurrentStop:       currStopName,
					PreviousStopID:    prevStopID,
					PreviousStop:      prevStopName,
					UpcomingStopIDs:   upcoming,
				})
			}
		}
	}
	return trains
}
