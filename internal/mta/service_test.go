package mta

import (
	"context"
	"path/filepath"
	"testing"
)

func TestServiceStationsAndHighStreetDefault(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	stations, routes := svc.GetStations()
	if len(stations) < 490 {
		t.Fatalf("expected at least 490 stations, got %d", len(stations))
	}
	if _, ok := routes["A"]; !ok {
		t.Fatalf("expected route A in routes map")
	}

	timesSq, ok := svc.GetStation("127")
	if !ok {
		t.Fatalf("expected Times Sq-42 St (127) in station map")
	}
	if timesSq.Name != "Times Sq-42 St" {
		t.Errorf("expected station name 'Times Sq-42 St', got %q", timesSq.Name)
	}

	// Verify simulated board for High St (A40) produces location-based arrivals and multi-track diagram
	board, err := svc.GetStationBoard(context.Background(), "A40", "", true)
	if err != nil {
		t.Fatalf("GetStationBoard simulated failed: %v", err)
	}
	if board.Station.ID != "A40" {
		t.Errorf("expected station A40, got %s", board.Station.ID)
	}
	if len(board.Northbound.Trains) == 0 || len(board.Southbound.Trains) == 0 {
		t.Fatalf("expected simulated trains in both directions for High St, got N=%d S=%d",
			len(board.Northbound.Trains), len(board.Southbound.Trains))
	}
	for _, tr := range board.Southbound.Trains {
		if tr.LocationStatus == "" || tr.StopsAwayLabel == "" {
			t.Errorf("expected non-empty LocationStatus and StopsAwayLabel, got %+v", tr)
		}
	}

	// Verify TrackMap data for High St (A=Express, C=Local)
	if !board.TrackMap.HasExpressTrack {
		t.Errorf("expected High St (A/C) TrackMap to have HasExpressTrack=true")
	}
	if len(board.TrackMap.Nodes) < 20 {
		t.Errorf("expected TrackMap spine to have at least 20 nodes, got %d", len(board.TrackMap.Nodes))
	}
	if len(board.TrackMap.Vehicles) == 0 {
		t.Errorf("expected TrackMap to have active vehicles")
	}

	// Verify Jay St-MetroTech (A41) separates A/C corridor from F corridor and includes R transfer link
	jayAC, err := svc.GetStationBoard(context.Background(), "A41", "A", true)
	if err != nil {
		t.Fatalf("GetStationBoard A41(A) failed: %v", err)
	}
	for _, tr := range jayAC.Southbound.Trains {
		if tr.RouteID == "F" {
			t.Errorf("expected A/C corridor at Jay St not to include F train, got %+v", tr)
		}
	}
	jayF, err := svc.GetStationBoard(context.Background(), "A41", "F", true)
	if err != nil {
		t.Fatalf("GetStationBoard A41(F) failed: %v", err)
	}
	for _, tr := range jayF.Southbound.Trains {
		if tr.RouteID != "F" {
			t.Errorf("expected F corridor at Jay St to only include F trains, got %s", tr.RouteID)
		}
	}
	if len(jayF.TransferLinks) < 3 {
		t.Errorf("expected at least 3 transfer corridor links (A/C, F, R) at Jay St-MetroTech, got %d", len(jayF.TransferLinks))
	}

	// Verify GetAllTrains returns system snapshot
	sysTrains, err := svc.GetAllTrains(context.Background())
	if err != nil {
		t.Fatalf("GetAllTrains failed: %v", err)
	}
	if len(sysTrains.Trains) == 0 {
		t.Fatalf("expected active trains in system snapshot, got 0")
	}
	for _, tr := range sysTrains.Trains {
		if tr.TripID == "" || tr.RouteID == "" || tr.CurrentStopID == "" {
			t.Errorf("expected valid train fields in system snapshot, got %+v", tr)
		}
	}
}

func TestUnifiedSystemTrainsAndProjection(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	// 1. Verify simulation produces rich trains across lines
	simTrains := svc.simulateSystemTrains()
	if len(simTrains) < 50 {
		t.Fatalf("expected at least 50 simulated trains across system, got %d", len(simTrains))
	}

	// Verify trains have required offline-first fields
	for _, tr := range simTrains {
		if tr.TripID == "" || tr.RouteID == "" || tr.Direction == "" || tr.CurrentStopID == "" {
			t.Errorf("train missing basic identity fields: %+v", tr)
		}
		if len(tr.UpcomingStops) == 0 {
			t.Errorf("train %s has no upcoming stops", tr.TripID)
		}
		if tr.UpcomingStops[0].StopID != tr.CurrentStopID {
			t.Errorf("train %s first upcoming stop %s != current stop %s",
				tr.TripID, tr.UpcomingStops[0].StopID, tr.CurrentStopID)
		}
		if tr.Track == "" || tr.TrackType == "" {
			t.Errorf("train %s missing track classification: track=%q, trackType=%q", tr.TripID, tr.Track, tr.TrackType)
		}
	}

	// 2. Verify projectStationArrivals projects correctly
	timesSq, ok := svc.GetStation("127")
	if !ok {
		t.Fatalf("expected station 127")
	}
	allowed := map[string]bool{"1": true, "2": true, "3": true}
	northArr, southArr := svc.projectStationArrivals(timesSq, allowed, simTrains)
	if len(northArr) == 0 || len(southArr) == 0 {
		t.Fatalf("expected arrivals at Times Sq in both directions, got N=%d S=%d", len(northArr), len(southArr))
	}
	for _, arr := range append(northArr, southArr...) {
		if arr.RouteID != "1" && arr.RouteID != "2" && arr.RouteID != "3" {
			t.Errorf("expected only 1/2/3 trains at Times Sq 7th Ave platform, got %s", arr.RouteID)
		}
		if arr.StopsAwayLabel == "" || arr.LocationStatus == "" {
			t.Errorf("missing stopsAwayLabel or locationStatus: %+v", arr)
		}
	}

	// 3. Verify caching behavior in getOrRefreshTrains
	snap1 := svc.getOrRefreshTrains(context.Background())
	if snap1 == nil || len(snap1.Trains) == 0 {
		t.Fatalf("expected non-empty snapshot from getOrRefreshTrains")
	}
	snap2 := svc.getOrRefreshTrains(context.Background())
	if snap1 != snap2 {
		t.Errorf("expected cached pointer to be returned within 12 seconds")
	}
}
