package mta

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestExtractNyctTrackInfo(t *testing.T) {
	// Build a synthetic NyctStopTimeUpdate protobuf message for Southbound 8th Ave (A30S):
	// Field 1 (scheduled_track): "3" (Southbound Express)
	// Field 2 (actual_track): "1" (Southbound Local)
	var subMsg []byte
	subMsg = protowire.AppendTag(subMsg, 1, protowire.BytesType)
	subMsg = protowire.AppendString(subMsg, "3")
	subMsg = protowire.AppendTag(subMsg, 2, protowire.BytesType)
	subMsg = protowire.AppendString(subMsg, "1")

	// Wrap inside extension 1001 (nyct_stop_time_update)
	var rawExt []byte
	rawExt = protowire.AppendTag(rawExt, 1001, protowire.BytesType)
	rawExt = protowire.AppendBytes(rawExt, subMsg)

	stu := &gtfs.TripUpdate_StopTimeUpdate{
		StopId: proto.String("A30S"),
	}
	stu.ProtoReflect().SetUnknown(rawExt)

	info := ExtractNyctTrackInfo(stu)
	if info.ScheduledTrack != "3" {
		t.Errorf("expected scheduled track '3', got %q", info.ScheduledTrack)
	}
	if info.ActualTrack != "1" {
		t.Errorf("expected actual track '1', got %q", info.ActualTrack)
	}

	// Verify ClassifyTrackType maps Track 1 to LOCAL and Track 3 to EXPRESS for route A
	if trackType := ClassifyTrackType(info.ActualTrack, "A"); trackType != "LOCAL" {
		t.Errorf("expected actual track '1' to classify as 'LOCAL', got %q", trackType)
	}
	if trackType := ClassifyTrackType(info.ScheduledTrack, "A"); trackType != "EXPRESS" {
		t.Errorf("expected scheduled track '3' to classify as 'EXPRESS', got %q", trackType)
	}
}

func TestClassifyTrackType(t *testing.T) {
	tests := []struct {
		track    string
		route    string
		expected string
	}{
		// B Division (IND / BMT): 1 & 2 are Local, 3 & 4 are Express
		{"1", "A", "LOCAL"},
		{"2", "A", "LOCAL"},
		{"3", "A", "EXPRESS"},
		{"4", "A", "EXPRESS"},
		{"TK1", "A", "LOCAL"},
		{"TK2", "A", "LOCAL"},
		{"TK3", "A", "EXPRESS"},
		{"TK4", "A", "EXPRESS"},
		{"A1", "A", "LOCAL"},
		{"A2", "A", "LOCAL"},
		{"A3", "A", "EXPRESS"},
		{"A4", "A", "EXPRESS"},
		{"B1", "B", "LOCAL"},
		{"B2", "B", "LOCAL"},
		{"B3", "B", "EXPRESS"},
		{"B4", "B", "EXPRESS"},
		{"M", "D", "EXPRESS"},

		// Chaining prefixes without route context explicitly passed
		{"A1", "", "LOCAL"},
		{"A2", "", "LOCAL"},
		{"A3", "", "EXPRESS"},
		{"A4", "", "EXPRESS"},
		{"B2", "", "LOCAL"},
		{"B4", "", "EXPRESS"},

		// A Division (IRT): 1 & 4 are Local, 2 & 3 are Express
		{"1", "1", "LOCAL"},
		{"2", "1", "EXPRESS"},
		{"3", "1", "EXPRESS"},
		{"4", "1", "LOCAL"},
		{"TK1", "1", "LOCAL"},
		{"TK2", "1", "EXPRESS"},
		{"TK3", "1", "EXPRESS"},
		{"TK4", "1", "LOCAL"},
		{"M", "1", "EXPRESS"},
		{"TKM", "7", "EXPRESS"},
		{"2", "IRT", "EXPRESS"},
		{"4", "IRT", "LOCAL"},

		// Explicit text keywords
		{"LOCAL", "", "LOCAL"},
		{"LOC", "", "LOCAL"},
		{"EXPRESS", "", "EXPRESS"},
		{"EXP", "", "EXPRESS"},
		{"", "", ""},
		{"UNKNOWN", "", ""},
	}

	for _, tc := range tests {
		got := ClassifyTrackType(tc.track, tc.route)
		if got != tc.expected {
			t.Errorf("ClassifyTrackType(%q, %q) = %q, expected %q", tc.track, tc.route, got, tc.expected)
		}
	}
}

func TestTrunkCorridorRoutes(t *testing.T) {
	// 23 St (C, E) should expand to include A on 8th Ave trunk
	eighthAv := TrunkCorridorRoutes([]string{"C", "E"})
	if !containsStr(eighthAv, "A") || !containsStr(eighthAv, "C") || !containsStr(eighthAv, "E") {
		t.Errorf("expected 8th Ave trunk routes to contain A, C, E; got %+v", eighthAv)
	}

	// 79 St (1) should expand to include 2, 3 on 7th Ave trunk
	seventhAv := TrunkCorridorRoutes([]string{"1"})
	if !containsStr(seventhAv, "1") || !containsStr(seventhAv, "2") || !containsStr(seventhAv, "3") {
		t.Errorf("expected 7th Ave trunk routes to contain 1, 2, 3; got %+v", seventhAv)
	}

	// Jay St A/C corridor should NOT include F
	jayAC := TrunkCorridorRoutes([]string{"A", "C"})
	if containsStr(jayAC, "F") {
		t.Errorf("expected Jay St A/C trunk corridor not to include F; got %+v", jayAC)
	}

	// Jay St F corridor should NOT include A or C
	jayF := TrunkCorridorRoutes([]string{"F"})
	if containsStr(jayF, "A") || containsStr(jayF, "C") {
		t.Errorf("expected Jay St F trunk corridor not to include A or C; got %+v", jayF)
	}
}

func TestExpressTrainRunningLocalTrackMapPlacement(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	st, ok := svc.GetStation("A30") // 23 St (local-only station on 8th Av)
	if !ok {
		t.Fatalf("expected station A30 (23 St)")
	}

	// Build a mock live feed with an A train (normally express) having actual_track = "1" (Local)
	// and stopping at 23 St (A30S)
	var subMsg []byte
	subMsg = protowire.AppendTag(subMsg, 1, protowire.BytesType)
	subMsg = protowire.AppendString(subMsg, "3") // scheduled express on Southbound 8th Ave
	subMsg = protowire.AppendTag(subMsg, 2, protowire.BytesType)
	subMsg = protowire.AppendString(subMsg, "1") // actual local track

	var rawExt []byte
	rawExt = protowire.AppendTag(rawExt, 1001, protowire.BytesType)
	rawExt = protowire.AppendBytes(rawExt, subMsg)

	stu23 := &gtfs.TripUpdate_StopTimeUpdate{
		StopId: proto.String("A30S"),
		Arrival: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(100),
		},
		Departure: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(120),
		},
	}
	stu23.ProtoReflect().SetUnknown(rawExt)

	stu14 := &gtfs.TripUpdate_StopTimeUpdate{
		StopId: proto.String("A31S"), // 14 St
		Arrival: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(200),
		},
	}

	tu := &gtfs.TripUpdate{
		Trip: &gtfs.TripDescriptor{
			TripId:  proto.String("A_TEST_REROUTE_LOCAL"),
			RouteId: proto.String("A"),
		},
		StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{stu23, stu14},
	}

	mockFeed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           proto.Uint64(105),
		},
		Entity: []*gtfs.FeedEntity{
			{
				Id:         proto.String("entity_1"),
				TripUpdate: tu,
			},
		},
	}

	// Build track map with live feed
	trunkRoutes := TrunkCorridorRoutes(st.Routes)
	tmap := svc.buildTrackMap(st, trunkRoutes, []*gtfs.FeedMessage{mockFeed}, false)

	if !tmap.HasExpressTrack {
		t.Errorf("expected 8th Ave track map at 23 St to have HasExpressTrack=true")
	}

	foundVehicle := false
	for _, v := range tmap.Vehicles {
		if v.TripID == "A_TEST_REROUTE_LOCAL" {
			foundVehicle = true
			if v.TrackType != "LOCAL" {
				t.Errorf("expected A train running on Track 1 to have TrackType='LOCAL', got %q", v.TrackType)
			}
			if v.Track != "1" {
				t.Errorf("expected Track='1', got %q", v.Track)
			}
		}
	}

	if !foundVehicle {
		t.Errorf("expected vehicle 'A_TEST_REROUTE_LOCAL' to be present in track map vehicles")
	}
}

func TestStationBoardIncludesDisruptedExpressTrainAtLocalStation(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	// Verify GetStationBoard expands allowedRoutesSet so that 23 St (normally C/E)
	// recognizes A train arrivals
	board, err := svc.GetStationBoard(context.Background(), "A30", "", true)
	if err != nil {
		t.Fatalf("GetStationBoard A30 failed: %v", err)
	}

	if !board.TrackMap.HasExpressTrack {
		t.Errorf("expected 23 St TrackMap to have HasExpressTrack=true")
	}
	if !containsStr(board.TrackMap.CorridorRoutes, "A") {
		t.Errorf("expected 23 St CorridorRoutes to include A; got %+v", board.TrackMap.CorridorRoutes)
	}
}

func TestNorthboundTracksAt14thStreet(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	st, ok := svc.GetStation("A31") // 14 St (express station on 8th Av)
	if !ok {
		t.Fatalf("expected station A31 (14 St)")
	}

	// Helper to create an NYCT stop time update extension with track info
	makeTrackExt := func(track string) []byte {
		var subMsg []byte
		subMsg = protowire.AppendTag(subMsg, 1, protowire.BytesType)
		subMsg = protowire.AppendString(subMsg, track)
		subMsg = protowire.AppendTag(subMsg, 2, protowire.BytesType)
		subMsg = protowire.AppendString(subMsg, track)

		var rawExt []byte
		rawExt = protowire.AppendTag(rawExt, 1001, protowire.BytesType)
		rawExt = protowire.AppendBytes(rawExt, subMsg)
		return rawExt
	}

	// Northbound E train stopped on Track 2 (Local)
	stuE14 := &gtfs.TripUpdate_StopTimeUpdate{
		StopId: proto.String("A31N"),
		Arrival: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(100),
		},
		Departure: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(120),
		},
	}
	stuE14.ProtoReflect().SetUnknown(makeTrackExt("2"))

	// Northbound A train stopped on Track 4 (Express)
	stuA14 := &gtfs.TripUpdate_StopTimeUpdate{
		StopId: proto.String("A31N"),
		Arrival: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(100),
		},
		Departure: &gtfs.TripUpdate_StopTimeEvent{
			Time: proto.Int64(120),
		},
	}
	stuA14.ProtoReflect().SetUnknown(makeTrackExt("4"))

	mockFeed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           proto.Uint64(105),
		},
		Entity: []*gtfs.FeedEntity{
			{
				Id: proto.String("e_train"),
				TripUpdate: &gtfs.TripUpdate{
					Trip: &gtfs.TripDescriptor{
						TripId:  proto.String("E_TRIP_NB"),
						RouteId: proto.String("E"),
					},
					StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{stuE14},
				},
			},
			{
				Id: proto.String("a_train"),
				TripUpdate: &gtfs.TripUpdate{
					Trip: &gtfs.TripDescriptor{
						TripId:  proto.String("A_TRIP_NB"),
						RouteId: proto.String("A"),
					},
					StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{stuA14},
				},
			},
		},
	}

	trunkRoutes := TrunkCorridorRoutes(st.Routes)
	tmap := svc.buildTrackMap(st, trunkRoutes, []*gtfs.FeedMessage{mockFeed}, false)

	vehMap := make(map[string]TrackVehicle)
	for _, v := range tmap.Vehicles {
		vehMap[v.TripID] = v
	}

	eVeh, ok := vehMap["E_TRIP_NB"]
	if !ok {
		t.Fatalf("expected E train vehicle in track map")
	}
	if eVeh.TrackType != "LOCAL" {
		t.Errorf("expected Northbound E train on Track 2 to have TrackType='LOCAL', got %q", eVeh.TrackType)
	}

	aVeh, ok := vehMap["A_TRIP_NB"]
	if !ok {
		t.Fatalf("expected A train vehicle in track map")
	}
	if aVeh.TrackType != "EXPRESS" {
		t.Errorf("expected Northbound A train on Track 4 to have TrackType='EXPRESS', got %q", aVeh.TrackType)
	}
}
