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
}
