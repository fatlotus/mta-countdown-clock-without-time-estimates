package mta

// Station represents an NYC Subway or SIR station from GTFS.
type Station struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Borough    string   `json:"borough"`
	Line       string   `json:"line"`
	Division   string   `json:"division"`
	ComplexID  string   `json:"complex_id"`
	NorthLabel string   `json:"north_label"`
	SouthLabel string   `json:"south_label"`
	Routes     []string `json:"routes"`
}

// RouteInfo represents an NYC Subway route and its official branding colors.
type RouteInfo struct {
	ID        string `json:"id"`
	ShortName string `json:"short_name"`
	LongName  string `json:"long_name"`
	Color     string `json:"color"`
	TextColor string `json:"text_color"`
}

// StaticData holds the embedded GTFS metadata for all 496 stations, routes, and canonical/daytime sequences.
type StaticData struct {
	Stations           []Station                    `json:"stations"`
	Routes             map[string]RouteInfo         `json:"routes"`
	CanonicalSequences map[string][]string          `json:"canonical_sequences"`
	DaytimeSequences   map[string][]string          `json:"daytime_sequences"`
	Spines             map[string]CorridorSpine     `json:"spines,omitempty"`
	StationCorridors   map[string][]StationCorridor `json:"station_corridors,omitempty"`
}

// CorridorSpine holds the precomputed static track diagram spine and station nodes for a line corridor.
type CorridorSpine struct {
	ID              string             `json:"id"`
	LineName        string             `json:"line_name"`
	CorridorRoutes  []string           `json:"corridor_routes"`
	LocalRoutes     []string           `json:"local_routes"`
	ExpressRoutes   []string           `json:"express_routes"`
	HasExpressTrack bool               `json:"has_express_track"`
	Nodes           []TrackDiagramNode `json:"nodes"`
}

// StationCorridor holds a station's precomputed corridor configuration, transfer links, and reference to its CorridorSpine.
type StationCorridor struct {
	FocusRoute    string         `json:"focus_route"`
	LineName      string         `json:"line_name"`
	ActiveRoutes  []string       `json:"active_routes"`
	TrunkRoutes   []string       `json:"trunk_routes"`
	SpineID       string         `json:"spine_id"`
	TargetIndex   int            `json:"target_index"`
	TransferLinks []TransferLink `json:"transfer_links"`
}

// UpcomingStop represents a future stop along an active train's trip.
type UpcomingStop struct {
	StopID      string `json:"stop_id"`
	ArrivalTime int64  `json:"arrival_time,omitempty"`
	Track       string `json:"track,omitempty"`
}

// TransferLink represents a clickable route/corridor link at a station or station complex
// so the user can switch between different lines/tracks serving that station (e.g., A/C vs F vs R at Jay St-MetroTech).
type TransferLink struct {
	StationID string   `json:"station_id"`
	LineName  string   `json:"line_name"`
	Routes    []string `json:"routes"`
	FocusRoute string  `json:"focus_route"`
	IsActive  bool     `json:"is_active"`
}

// TrainArrival represents an upcoming train for a station board, replacing time-based arrival
// countdowns with the train's live physical position along the line.
type TrainArrival struct {
	TripID            string   `json:"trip_id"`
	RouteID           string   `json:"route_id"`
	RouteShortName    string   `json:"route_short_name"`
	RouteColor        string   `json:"route_color"`
	RouteTextColor    string   `json:"route_text_color"`
	Direction         string   `json:"direction"`       // "N" or "S"
	Destination       string   `json:"destination"`     // Terminal station name (e.g., "Inwood-207 St")
	DestinationStopID string   `json:"destination_stop_id,omitempty"`
	LocationStatus    string   `json:"location_status"` // e.g., "At Jay St-MetroTech", "Between Chambers St & Fulton St"
	StatusType        string   `json:"status_type"`     // "AT_STATION", "APPROACHING", "BETWEEN", "AT_TARGET"
	CurrentStopID     string   `json:"current_stop_id"`
	CurrentStop       string   `json:"current_stop"`
	PreviousStopID    string   `json:"previous_stop_id,omitempty"`
	PreviousStop      string   `json:"previous_stop,omitempty"`
	StopsAway         int      `json:"stops_away"`
	StopsAwayLabel    string   `json:"stops_away_label"` // e.g., "In Station", "1 stop away", "4 stops away"
	Track             string   `json:"track,omitempty"`  // Physical track identifier (e.g., "1", "2", "3", "4", "M")
	RecentPath        []string `json:"recent_path"`      // Sequence of stops leading to the target station for strip map visualization
	RecentStopIDs     []string `json:"recent_stop_ids,omitempty"`
	SortOrder         int64    `json:"-"`
}

// DirectionBoard holds the arrivals and metadata for one direction (Northbound or Southbound) at a station.
type DirectionBoard struct {
	Direction   string         `json:"direction"`
	Label       string         `json:"label"`
	Trains      []TrainArrival `json:"trains"`
	ServiceNote string         `json:"service_note,omitempty"`
}

// TrackDiagramNode represents one station row along the vertical line corridor in Track Map Mode.
type TrackDiagramNode struct {
	StopID            string         `json:"stop_id"`
	Name              string         `json:"name"`
	Borough           string         `json:"borough"`
	IsTarget          bool           `json:"is_target"`
	OffsetFromFocus   int            `json:"offset_from_focus"`   // Negative = North of target, 0 = Target, Positive = South of target
	LocalRoutes       []string       `json:"local_routes"`        // Routes on the local track that stop here
	ExpressRoutes     []string       `json:"express_routes"`      // Express routes that stop here (empty if express bypasses this stop)
	AllStopRoutes     []string       `json:"all_stop_routes"`     // Corridor routes that stop here
	OtherLinks        []TransferLink `json:"other_links"`         // Other lines/tracks at this station or complex (e.g., F or R at Jay St)
	IsMergedTrack     bool           `json:"is_merged_track"`     // True when Local & Express share a single combined track (e.g. Chambers St – High St)
	HasExpressBypass  bool           `json:"has_express_bypass"`  // True when an Express track exists and bypasses this Local-only station
	IsExpressStation  bool           `json:"is_express_station"`  // True when separate Local and Express tracks both stop at this station (e.g. 14 St, Canal St)
	NextSegmentTracks string         `json:"next_segment_tracks"` // "MERGED" (single combined rail to next stop) or "SPLIT" (parallel Local + Express rails to next stop)
}

// TrackVehicle represents an active train positioned either AT a station node or BETWEEN two station nodes
// on the Local, Express, or Merged track in Track Map Mode.
type TrackVehicle struct {
	TripID         string `json:"trip_id"`
	RouteID        string `json:"route_id"`
	RouteShortName string `json:"route_short_name"`
	RouteColor     string `json:"route_color"`
	RouteTextColor string `json:"route_text_color"`
	Direction      string `json:"direction"`   // "N" or "S"
	TrackType      string `json:"track_type"`  // "LOCAL", "EXPRESS", or "MERGED"
	Track          string `json:"track,omitempty"` // Physical track identifier (e.g. "1", "2", "3", "4", "M")
	Placement      string `json:"placement"`   // "AT_STOP" or "BETWEEN_STOPS"
	StopID         string `json:"stop_id"`     // Spine stop ID where train is stopped or approaching
	FromStopID     string `json:"from_stop_id,omitempty"`
	ToStopID       string `json:"to_stop_id,omitempty"`
	NodeIndex      int    `json:"node_index"`  // Index in TrackMap.Nodes (for AT_STOP, or upper node index for BETWEEN_STOPS)
	StatusLabel    string `json:"status_label"`
	Destination    string `json:"destination"`
}

// TrackMapData contains the vertical corridor diagram with converging/diverging Local & Express tracks,
// station platform bars, and train car tokens.
type TrackMapData struct {
	LineName        string             `json:"line_name"`
	NorthLabel      string             `json:"north_label"`
	SouthLabel      string             `json:"south_label"`
	CorridorRoutes  []string           `json:"corridor_routes"`
	LocalRoutes     []string           `json:"local_routes"`
	ExpressRoutes   []string           `json:"express_routes"`
	HasExpressTrack bool               `json:"has_express_track"`
	TargetIndex     int                `json:"target_index"`
	Nodes           []TrackDiagramNode `json:"nodes"`
	Vehicles        []TrackVehicle     `json:"vehicles"`
}

// StationBoardResponse is the API response for a single station's countdown clock and live track map.
type StationBoardResponse struct {
	Station       Station              `json:"station"`
	ActiveRoute   string               `json:"active_route,omitempty"`
	ActiveRoutes  []string             `json:"active_routes"`
	TransferLinks []TransferLink       `json:"transfer_links"`
	RouteMetadata map[string]RouteInfo `json:"route_metadata"`
	Northbound    DirectionBoard       `json:"northbound"`
	Southbound    DirectionBoard       `json:"southbound"`
	TrackMap      TrackMapData         `json:"track_map"`
	DataSource    string               `json:"data_source"` // "LIVE_MTA_GTFS_RT" or "SIMULATED_FALLBACK"
	LastUpdated   string               `json:"last_updated"`
	FeedTimestamp int64                `json:"feed_timestamp"`
}

// SystemTrain represents an active train across the entire NYC subway system for offline caching.
type SystemTrain struct {
	TripID            string         `json:"trip_id"`
	RouteID           string         `json:"route_id"`
	RouteShortName    string         `json:"route_short_name"`
	RouteColor        string         `json:"route_color"`
	RouteTextColor    string         `json:"route_text_color"`
	Direction         string         `json:"direction"`
	Destination       string         `json:"destination"`
	DestinationStopID string         `json:"destination_stop_id,omitempty"`
	StatusType        string         `json:"status_type"`
	CurrentStopID     string         `json:"current_stop_id"`
	CurrentStop       string         `json:"current_stop"`
	PreviousStopID    string         `json:"previous_stop_id,omitempty"`
	PreviousStop      string         `json:"previous_stop,omitempty"`
	UpcomingStopIDs   []string       `json:"upcoming_stop_ids"`
	Track             string         `json:"track,omitempty"`
	TrackType         string         `json:"track_type,omitempty"`
	LocationStatus    string         `json:"location_status,omitempty"`
	ArrDiff           int64          `json:"arr_diff"`
	DepDiff           int64          `json:"dep_diff"`
	UpcomingStops     []UpcomingStop `json:"upcoming_stops,omitempty"`
}

// SystemTrainsResponse holds all active trains in the system for offline browsing across all 496 stations.
type SystemTrainsResponse struct {
	DataSource    string        `json:"data_source"`
	FeedTimestamp int64         `json:"feed_timestamp"`
	LastUpdated   string        `json:"last_updated"`
	Trains        []SystemTrain `json:"trains"`
}
