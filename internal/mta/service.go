package mta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

var feedURLs = map[string]string{
	"gtfs-ace":  "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-ace",
	"gtfs-bdfm": "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-bdfm",
	"gtfs-g":    "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-g",
	"gtfs-jz":   "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-jz",
	"gtfs-nqrw": "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-nqrw",
	"gtfs-l":    "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-l",
	"gtfs":      "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs",
	"gtfs-si":   "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-si",
	"alerts":    "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/camsys%2Fsubway-alerts",
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

	mu              sync.RWMutex
	feedCache       map[string]cachedFeed
	cachedTrains    *SystemTrainsResponse
	trainsFetchedAt time.Time

	alertsSnapshot  atomic.Pointer[SystemAlertsResponse]
	alertsRefreshMu sync.Mutex
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

	svc := &Service{
		static:     static,
		stationMap: stationMap,
		complexMap: complexMap,
		stopNames:  stopNames,
		httpClient: &http.Client{Timeout: 6 * time.Second},
		feedCache:  make(map[string]cachedFeed),
	}
	svc.precomputeTopology()
	return svc, nil
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

func (s *Service) fetchFeed(ctx context.Context, feedName string) (*gtfs.FeedMessage, error) {
	s.mu.RLock()
	cached, ok := s.feedCache[feedName]
	s.mu.RUnlock()
	if ok && time.Since(cached.fetchedAt) < 15*time.Second && cached.msg != nil {
		return cached.msg, nil
	}

	url, okURL := feedURLs[feedName]
	if !okURL {
		url = fmt.Sprintf("https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%%2F%s", feedName)
	}
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

	sc, tmap := s.getStationCorridor(st, focusRoute)
	activeRoutes := sc.ActiveRoutes
	transferLinks := sc.TransferLinks
	trunkRoutes := sc.TrunkRoutes
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
		TrackMap:    tmap,
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}

	var trains []SystemTrain
	if forceSimulate {
		trains = s.simulateSystemTrains()
		resp.DataSource = "SIMULATED_FALLBACK"
		resp.FeedTimestamp = time.Now().Unix()
	} else {
		sysSnapshot := s.getOrRefreshTrains(ctx)
		resp.DataSource = sysSnapshot.DataSource
		resp.FeedTimestamp = sysSnapshot.FeedTimestamp
		trains = sysSnapshot.Trains
	}

	northTrains, southTrains := s.projectStationArrivals(st, allowedRoutesSet, trains)
	resp.Northbound.Trains = northTrains
	resp.Southbound.Trains = southTrains
	resp.TrackMap.Vehicles = s.projectTrackVehicles(tmap, trains)

	var alerts []ServiceAlert
	if forceSimulate {
		if snap := s.alertsSnapshot.Load(); snap != nil {
			alerts = snap.Alerts
		}
	} else {
		alertsResp := s.getOrRefreshAlerts(ctx)
		if alertsResp != nil {
			alerts = alertsResp.Alerts
		}
	}

	nowSec := time.Now().Unix()
	stationAlerts, northAlerts, southAlerts := s.projectStationAlerts(st, allowedRoutesSet, alerts, nowSec)
	resp.Alerts = stationAlerts
	resp.Northbound.Alerts = northAlerts
	resp.Southbound.Alerts = southAlerts

	if !forceSimulate {
		if len(northTrains) == 0 {
			if len(northAlerts) > 0 {
				resp.Northbound.ServiceNote = northAlerts[0].HeaderText
			} else if st.NorthLabel != "" {
				resp.Northbound.ServiceNote = "No active trains currently scheduled for this platform in live MTA feed (possible reroute or off-hours service change)."
			}
		}
		if len(southTrains) == 0 {
			if len(southAlerts) > 0 {
				resp.Southbound.ServiceNote = southAlerts[0].HeaderText
			} else if st.SouthLabel != "" {
				resp.Southbound.ServiceNote = "No active trains currently scheduled for this platform in live MTA feed (possible reroute or off-hours service change)."
			}
		}
	}

	return resp, nil
}

// projectStationArrivals projects system trains into arrivals at a target station,
// filtered to allowed routes and sorted by proximity and arrival time.
func (s *Service) projectStationArrivals(st Station, allowedRoutesSet map[string]bool, trains []SystemTrain) ([]TrainArrival, []TrainArrival) {
	northTrains := []TrainArrival{}
	southTrains := []TrainArrival{}

	for _, tr := range trains {
		if len(allowedRoutesSet) > 0 && !allowedRoutesSet[tr.RouteID] && !allowedRoutesSet[s.normalizeRouteID(tr.RouteID)] {
			continue
		}

		targetIdx := -1
		for idx, us := range tr.UpcomingStops {
			if us.StopID == st.ID {
				targetIdx = idx
				break
			}
		}
		if targetIdx == -1 {
			continue
		}

		targetStop := tr.UpcomingStops[targetIdx]
		stopsAway := targetIdx
		var statusType, locationStatus, stopsAwayLabel string

		if stopsAway == 0 {
			if tr.StatusType == "AT_STATION" {
				locationStatus = fmt.Sprintf("At %s (Boarding in Station)", st.Name)
				statusType = "AT_TARGET"
				stopsAwayLabel = "In Station"
			} else {
				locationStatus = fmt.Sprintf("Approaching %s", st.Name)
				statusType = "APPROACHING"
				stopsAwayLabel = "Entering Station"
			}
		} else {
			statusType = tr.StatusType
			locationStatus = tr.LocationStatus
			if stopsAway == 1 {
				stopsAwayLabel = "1 stop away"
			} else {
				stopsAwayLabel = fmt.Sprintf("%d stops away", stopsAway)
			}
		}

		var recentPath []string
		var recentStopIDs []string
		startIdx := 0
		if targetIdx > 4 {
			startIdx = targetIdx - 4
		}
		if startIdx > 0 {
			recentPath = append(recentPath, tr.CurrentStop, "...")
			recentStopIDs = append(recentStopIDs, tr.CurrentStopID, "")
		}
		for k := startIdx; k <= targetIdx; k++ {
			sid := tr.UpcomingStops[k].StopID
			sname := s.stopNames[sid]
			if sname == "" {
				sname = sid
			}
			recentPath = append(recentPath, sname)
			recentStopIDs = append(recentStopIDs, sid)
		}

		arrTrack := targetStop.Track
		if arrTrack == "" {
			arrTrack = tr.Track
		}

		targetTime := targetStop.ArrivalTime
		if targetTime == 0 {
			targetTime = time.Now().Unix() + int64(stopsAway)*90
		}

		arr := TrainArrival{
			TripID:            tr.TripID,
			RouteID:           tr.RouteID,
			RouteShortName:    tr.RouteShortName,
			RouteColor:        tr.RouteColor,
			RouteTextColor:    tr.RouteTextColor,
			Direction:         tr.Direction,
			Destination:       tr.Destination,
			DestinationStopID: tr.DestinationStopID,
			LocationStatus:    locationStatus,
			StatusType:        statusType,
			CurrentStopID:     tr.CurrentStopID,
			CurrentStop:       tr.CurrentStop,
			PreviousStopID:    tr.PreviousStopID,
			PreviousStop:      tr.PreviousStop,
			StopsAway:         stopsAway,
			StopsAwayLabel:    stopsAwayLabel,
			Track:             arrTrack,
			RecentPath:        recentPath,
			RecentStopIDs:     recentStopIDs,
			SortOrder:         targetTime,
		}

		if tr.Direction == "N" {
			northTrains = append(northTrains, arr)
		} else {
			southTrains = append(southTrains, arr)
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

	return northTrains, southTrains
}

// parseFeedsToTrains parses GTFS-RT feed messages into a unified slice of SystemTrain.
func (s *Service) parseFeedsToTrains(validFeeds []*gtfs.FeedMessage) ([]SystemTrain, int64) {
	var latestTS int64
	for _, feed := range validFeeds {
		if feed != nil && feed.Header != nil && feed.Header.Timestamp != nil {
			ts := int64(feed.Header.GetTimestamp())
			if ts > latestTS {
				latestTS = ts
			}
		}
	}
	if latestTS == 0 {
		latestTS = time.Now().Unix()
	}

	vehMap := make(map[string]*gtfs.VehiclePosition)
	for _, feed := range validFeeds {
		if feed == nil {
			continue
		}
		for _, entity := range feed.GetEntity() {
			if veh := entity.GetVehicle(); veh != nil {
				if trip := veh.GetTrip(); trip != nil && trip.GetTripId() != "" {
					vehMap[trip.GetTripId()] = veh
				}
			}
		}
	}

	var trains []SystemTrain
	seenTrips := make(map[string]bool)

	for _, feed := range validFeeds {
		if feed == nil {
			continue
		}
		feedTS := int64(feed.GetHeader().GetTimestamp())
		if feedTS == 0 {
			feedTS = latestTS
		}

		for _, entity := range feed.GetEntity() {
			tu := entity.GetTripUpdate()
			if tu == nil {
				continue
			}
			trip := tu.GetTrip()
			if trip == nil {
				continue
			}
			tripID := trip.GetTripId()
			if tripID == "" || seenTrips[tripID] {
				continue
			}
			seenTrips[tripID] = true

			routeID := trip.GetRouteId()
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
				if firstTime > 0 && firstTime-feedTS > 600 {
					continue
				}
			}

			currStopRaw := stus[currIdx].GetStopId()
			currStopID := CleanStopID(currStopRaw)
			currStopName := s.stopNames[currStopID]
			if currStopName == "" {
				currStopName = currStopID
			}

			dir := ""
			if strings.HasSuffix(currStopRaw, "N") {
				dir = "N"
			} else if strings.HasSuffix(currStopRaw, "S") {
				dir = "S"
			}
			if dir == "" {
				if currIdx+1 < len(stus) {
					nextStopID := CleanStopID(stus[currIdx+1].GetStopId())
					seqN := s.static.CanonicalSequences[routeID+"_N"]
					for idx, sid := range seqN {
						if sid == currStopID && idx+1 < len(seqN) && seqN[idx+1] == nextStopID {
							dir = "N"
							break
						}
					}
				}
				if dir == "" {
					dir = "S"
				}
			}

			destStopRaw := stus[len(stus)-1].GetStopId()
			destStopID := CleanStopID(destStopRaw)
			destName := s.stopNames[destStopID]
			if destName == "" {
				destName = destStopID
			}

			rInfo := s.getRouteInfo(routeID)
			veh := vehMap[tripID]

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

			currTrackInfo := ExtractNyctTrackInfo(stus[currIdx])
			var nextTrackInfo NyctTrackInfo
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
			var statusType, locationStatus string

			if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_STOPPED_AT) || (arrDiff <= 15 && depDiff >= -25) {
				statusType = "AT_STATION"
				locationStatus = fmt.Sprintf("At %s", currStopName)
			} else if (vehAtCurr && veh.GetCurrentStatus() == gtfs.VehiclePosition_INCOMING_AT) || (arrDiff > 15 && arrDiff <= 45) {
				statusType = "APPROACHING"
				locationStatus = fmt.Sprintf("Approaching %s", currStopName)
			} else if prevStopName != "" && prevStopName != currStopName {
				statusType = "BETWEEN"
				locationStatus = fmt.Sprintf("Between %s & %s", prevStopName, currStopName)
			} else {
				statusType = "APPROACHING"
				locationStatus = fmt.Sprintf("Approaching %s", currStopName)
			}

			var upcomingStopIDs []string
			var upcomingStops []UpcomingStop
			for k := currIdx; k < len(stus); k++ {
				sid := CleanStopID(stus[k].GetStopId())
				arrT := stus[k].GetArrival().GetTime()
				if arrT == 0 {
					arrT = stus[k].GetDeparture().GetTime()
				}
				ti := ExtractNyctTrackInfo(stus[k])
				stTrack := ti.ActualTrack
				if stTrack == "" {
					stTrack = ti.ScheduledTrack
				}
				if stTrack == "" {
					stTrack = reportedTrack
				}
				upcomingStopIDs = append(upcomingStopIDs, sid)
				upcomingStops = append(upcomingStops, UpcomingStop{
					StopID:      sid,
					ArrivalTime: arrT,
					Track:       stTrack,
				})
			}

			trains = append(trains, SystemTrain{
				TripID:            tripID,
				RouteID:           routeID,
				RouteShortName:    rInfo.ShortName,
				RouteColor:        rInfo.Color,
				RouteTextColor:    rInfo.TextColor,
				Direction:         dir,
				Destination:       destName,
				DestinationStopID: destStopID,
				StatusType:        statusType,
				LocationStatus:    locationStatus,
				CurrentStopID:     currStopID,
				CurrentStop:       currStopName,
				PreviousStopID:    prevStopID,
				PreviousStop:      prevStopName,
				UpcomingStopIDs:   upcomingStopIDs,
				UpcomingStops:     upcomingStops,
				Track:             reportedTrack,
				TrackType:         trackType,
				ArrDiff:           arrDiff,
				DepDiff:           depDiff,
			})
		}
	}

	return trains, latestTS
}

// simulateSystemTrains synthesizes realistic system trains across all MTA subway lines
// when offline or when simulation mode is active.
func (s *Service) simulateSystemTrains() []SystemTrain {
	var trains []SystemTrain
	timeBucket := time.Now().Unix() / 20
	routes := []string{
		"1", "2", "3", "4", "5", "6", "7",
		"A", "C", "E", "B", "D", "F", "M",
		"G", "J", "Z", "L", "N", "Q", "R", "W",
		"GS", "FS", "H", "SI",
	}

	for _, r := range routes {
		for _, dir := range []string{"N", "S"} {
			seq := s.static.CanonicalSequences[r+"_"+dir]
			if len(seq) < 2 {
				continue
			}
			rInfo := s.getRouteInfo(r)
			destStopID := seq[len(seq)-1]
			destName := s.stopNames[destStopID]
			if destName == "" {
				destName = destStopID
			}

			step := 4
			if len(seq) < 8 {
				step = 2
			}
			startPos := int(timeBucket % int64(step))
			var positions []int
			for pos := startPos; pos < len(seq)-1; pos += step {
				positions = append(positions, pos)
			}
			if len(positions) == 0 || positions[0] != 0 {
				positions = append([]int{0}, positions...)
			}

			trackType := "LOCAL"
			if defaultExpressRoutes[r] {
				trackType = "EXPRESS"
			}
			track := "1"
			switch r {
			case "1", "2", "3", "4", "5", "6", "7":
				if trackType == "EXPRESS" {
					if dir == "N" {
						track = "3"
					} else {
						track = "2"
					}
				} else {
					if dir == "N" {
						track = "4"
					} else {
						track = "1"
					}
				}
			default:
				if trackType == "EXPRESS" {
					if dir == "N" {
						track = "4"
					} else {
						track = "3"
					}
				} else {
					if dir == "N" {
						track = "2"
					} else {
						track = "1"
					}
				}
			}

			now := time.Now().Unix()
			for _, pos := range positions {
				currStopID := seq[pos]
				currStopName := s.stopNames[currStopID]
				if currStopName == "" {
					currStopName = currStopID
				}
				prevStopID := ""
				prevStopName := ""
				if pos > 0 {
					prevStopID = seq[pos-1]
					prevStopName = s.stopNames[prevStopID]
				}

				var upcoming []string
				var upcomingStops []UpcomingStop
				for k := pos; k < len(seq); k++ {
					upcoming = append(upcoming, seq[k])
					upcomingStops = append(upcomingStops, UpcomingStop{
						StopID:      seq[k],
						ArrivalTime: now + int64((k-pos)*90),
						Track:       track,
					})
				}

				statusType := "APPROACHING"
				locationStatus := "Approaching " + currStopName
				arrDiff := int64(30)
				depDiff := int64(90)
				if pos == 0 || (timeBucket+int64(pos))%2 == 0 {
					statusType = "AT_STATION"
					locationStatus = "At " + currStopName
					arrDiff = 0
					depDiff = 20
				} else if prevStopName != "" {
					statusType = "BETWEEN"
					locationStatus = "Between " + prevStopName + " & " + currStopName
					arrDiff = 45
					depDiff = 105
				}

				trains = append(trains, SystemTrain{
					TripID:            fmt.Sprintf("SIM_%s_%s_%d", r, dir, pos),
					RouteID:           r,
					RouteShortName:    rInfo.ShortName,
					RouteColor:        rInfo.Color,
					RouteTextColor:    rInfo.TextColor,
					Direction:         dir,
					Destination:       destName,
					DestinationStopID: destStopID,
					StatusType:        statusType,
					LocationStatus:    locationStatus,
					CurrentStopID:     currStopID,
					CurrentStop:       currStopName,
					PreviousStopID:    prevStopID,
					PreviousStop:      prevStopName,
					Track:             track,
					TrackType:         trackType,
					ArrDiff:           arrDiff,
					DepDiff:           depDiff,
					UpcomingStopIDs:   upcoming,
					UpcomingStops:     upcomingStops,
				})
			}
		}
	}

	return trains
}

// fetchFeeds fetches multiple MTA GTFS-RT feeds concurrently.
func (s *Service) fetchFeeds(ctx context.Context, feeds []string) []*gtfs.FeedMessage {
	type feedResult struct {
		msg *gtfs.FeedMessage
		err error
	}
	results := make([]feedResult, len(feeds))
	var wg sync.WaitGroup
	for i, fn := range feeds {
		wg.Add(1)
		go func(idx int, feedName string) {
			defer wg.Done()
			msg, err := s.fetchFeed(ctx, feedName)
			results[idx] = feedResult{msg: msg, err: err}
		}(i, fn)
	}
	wg.Wait()

	var validFeeds []*gtfs.FeedMessage
	for _, r := range results {
		if r.msg != nil {
			validFeeds = append(validFeeds, r.msg)
		}
	}
	return validFeeds
}

// refreshTrains unconditionally fetches all feeds, parses active trains, and updates the cached snapshot.
func (s *Service) refreshTrains(ctx context.Context) *SystemTrainsResponse {
	validFeeds := s.fetchFeeds(ctx, allFeeds)
	now := time.Now().UTC().Format(time.RFC3339)
	var resp *SystemTrainsResponse

	if len(validFeeds) > 0 {
		trains, latestTS := s.parseFeedsToTrains(validFeeds)
		if latestTS == 0 {
			latestTS = time.Now().Unix()
		}
		resp = &SystemTrainsResponse{
			LastUpdated:   now,
			DataSource:    "LIVE_MTA_GTFS_RT",
			FeedTimestamp: latestTS,
			Trains:        trains,
		}
	} else {
		simTrains := s.simulateSystemTrains()
		resp = &SystemTrainsResponse{
			LastUpdated:   now,
			DataSource:    "SIMULATED_FALLBACK",
			FeedTimestamp: time.Now().Unix(),
			Trains:        simTrains,
		}
	}

	s.mu.Lock()
	s.cachedTrains = resp
	s.trainsFetchedAt = time.Now()
	s.mu.Unlock()

	return resp
}

// getOrRefreshTrains returns the cached system train snapshot, refreshing from live feeds
// if the cache is uninitialized or older than 12 seconds.
func (s *Service) getOrRefreshTrains(ctx context.Context) *SystemTrainsResponse {
	s.mu.RLock()
	if s.cachedTrains != nil && time.Since(s.trainsFetchedAt) < 12*time.Second {
		resp := s.cachedTrains
		s.mu.RUnlock()
		return resp
	}
	s.mu.RUnlock()

	return s.refreshTrains(ctx)
}

// GetAllTrains returns a system-wide snapshot of all active trains.
func (s *Service) GetAllTrains(ctx context.Context) (*SystemTrainsResponse, error) {
	return s.getOrRefreshTrains(ctx), nil
}

// refreshAlerts fetches the live alerts feed, parses active/upcoming alerts, and stores the snapshot.
func (s *Service) refreshAlerts(ctx context.Context) *SystemAlertsResponse {
	feed, err := s.fetchFeed(ctx, "alerts")
	nowSec := time.Now().Unix()
	now := time.Now().UTC().Format(time.RFC3339)

	var alerts []ServiceAlert
	feedTS := nowSec

	if err == nil && feed != nil {
		parsedAlerts, ts := parseFeedToAlerts(feed, nowSec)
		alerts = parsedAlerts
		if ts != 0 {
			feedTS = ts
		}
	} else {
		// If fetch failed, preserve previous snapshot if available
		if existing := s.alertsSnapshot.Load(); existing != nil {
			return existing
		}
	}

	if alerts == nil {
		alerts = []ServiceAlert{}
	}

	resp := &SystemAlertsResponse{
		LastUpdated:   now,
		FeedTimestamp: feedTS,
		Alerts:        alerts,
	}
	s.alertsSnapshot.Store(resp)
	return resp
}

// getOrRefreshAlerts returns the cached service alerts snapshot, refreshing if uninitialized
// or older than 60 seconds.
func (s *Service) getOrRefreshAlerts(ctx context.Context) *SystemAlertsResponse {
	snap := s.alertsSnapshot.Load()
	if snap != nil {
		if t, err := time.Parse(time.RFC3339, snap.LastUpdated); err == nil && time.Since(t) < 60*time.Second {
			return snap
		}
	}

	s.alertsRefreshMu.Lock()
	defer s.alertsRefreshMu.Unlock()

	snap = s.alertsSnapshot.Load()
	if snap != nil {
		if t, err := time.Parse(time.RFC3339, snap.LastUpdated); err == nil && time.Since(t) < 60*time.Second {
			return snap
		}
	}

	return s.refreshAlerts(ctx)
}

// GetAllAlerts returns the system-wide snapshot of all active and upcoming service alerts.
func (s *Service) GetAllAlerts(ctx context.Context) (*SystemAlertsResponse, error) {
	return s.getOrRefreshAlerts(ctx), nil
}

// StartBackgroundSync populates the system train and alert caches immediately and refreshes them periodically.
func (s *Service) StartBackgroundSync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		s.refreshTrains(ctx)
		s.refreshAlerts(ctx)

		trainTicker := time.NewTicker(interval)
		defer trainTicker.Stop()
		alertTicker := time.NewTicker(60 * time.Second)
		defer alertTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-trainTicker.C:
				s.refreshTrains(ctx)
			case <-alertTicker.C:
				s.refreshAlerts(ctx)
			}
		}
	}()
}
