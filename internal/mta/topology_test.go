package mta

import (
	"path/filepath"
	"testing"
)

func TestPrecomputedTopologyAtStartup(t *testing.T) {
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	staticData := svc.GetStaticData()

	if len(staticData.Spines) == 0 {
		t.Fatalf("expected non-empty static Spines map, got %d", len(staticData.Spines))
	}

	if len(staticData.StationCorridors) != len(staticData.Stations) {
		t.Fatalf("expected StationCorridors for 100%% of stations (%d), got %d",
			len(staticData.Stations), len(staticData.StationCorridors))
	}

	// Verify all referenced SpineIDs exist and have valid structure
	for stID, corridors := range staticData.StationCorridors {
		if len(corridors) == 0 {
			t.Errorf("station %s has 0 corridors", stID)
			continue
		}
		for _, corr := range corridors {
			spine, exists := staticData.Spines[corr.SpineID]
			if !exists {
				t.Errorf("station %s references non-existent SpineID %q", stID, corr.SpineID)
				continue
			}
			if len(spine.Nodes) == 0 {
				t.Errorf("spine %s has 0 nodes", spine.ID)
			}
			if corr.TargetIndex < 0 || corr.TargetIndex >= len(spine.Nodes) {
				t.Errorf("station %s target index %d out of bounds for spine %s (len %d)",
					stID, corr.TargetIndex, spine.ID, len(spine.Nodes))
			}
		}
	}

	// Verify Jay St-MetroTech (A41) multi-corridor behavior
	jaySt, ok := svc.GetStation("A41")
	if !ok {
		t.Fatalf("station A41 not found")
	}

	scAC, tmapAC := svc.getStationCorridor(jaySt, "A")
	if !containsStr(scAC.ActiveRoutes, "A") || !containsStr(scAC.ActiveRoutes, "C") {
		t.Errorf("expected A/C active routes for Jay St focus=A, got %+v", scAC.ActiveRoutes)
	}
	if scAC.TargetIndex < 0 || scAC.TargetIndex >= len(tmapAC.Nodes) {
		t.Fatalf("target index %d out of bounds", scAC.TargetIndex)
	}
	if !tmapAC.Nodes[scAC.TargetIndex].IsTarget {
		t.Errorf("expected target node to have IsTarget=true")
	}
	if tmapAC.Nodes[scAC.TargetIndex].StopID != "A41" {
		t.Errorf("expected target node StopID 'A41', got %s", tmapAC.Nodes[scAC.TargetIndex].StopID)
	}
	if tmapAC.Nodes[scAC.TargetIndex].OffsetFromFocus != 0 {
		t.Errorf("expected target node OffsetFromFocus=0, got %d", tmapAC.Nodes[scAC.TargetIndex].OffsetFromFocus)
	}

	scF, tmapF := svc.getStationCorridor(jaySt, "F")
	if !containsStr(scF.ActiveRoutes, "F") || containsStr(scF.ActiveRoutes, "A") {
		t.Errorf("expected F-only active routes for Jay St focus=F, got %+v", scF.ActiveRoutes)
	}
	if !tmapF.Nodes[scF.TargetIndex].IsTarget {
		t.Errorf("expected target node for F corridor to have IsTarget=true")
	}
	if tmapF.Nodes[scF.TargetIndex].StopID != "A41" {
		t.Errorf("expected target node StopID 'A41', got %s", tmapF.Nodes[scF.TargetIndex].StopID)
	}

	// Verify Times Square-42 St (127) corridor
	timesSq, ok := svc.GetStation("127")
	if !ok {
		t.Fatalf("station 127 not found")
	}
	sc127, tmap127 := svc.getStationCorridor(timesSq, "")
	if len(sc127.ActiveRoutes) == 0 {
		t.Errorf("expected active routes for Times Sq")
	}
	if !tmap127.Nodes[sc127.TargetIndex].IsTarget {
		t.Errorf("expected target node IsTarget=true for Times Sq")
	}

	// Verify fallback when focus route is unknown/fallback
	scFallback, _ := svc.getStationCorridor(jaySt, "UNKNOWN")
	if scFallback.FocusRoute != scAC.FocusRoute {
		t.Errorf("expected unknown focus route to fall back to index 0 corridor, got %+v", scFallback)
	}
}
