package mta

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

func strPtr(s string) *string {
	return &s
}

func uint32Ptr(u uint32) *uint32 {
	return &u
}

func uint64Ptr(u uint64) *uint64 {
	return &u
}

func TestAlertsFeedParsingAndStationProjection(t *testing.T) {
	nowSec := time.Now().Unix()

	feed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: strPtr("2.0"),
			Timestamp:           uint64Ptr(uint64(nowSec)),
		},
		Entity: []*gtfs.FeedEntity{
			// 1. High St Northbound-only planned work alert (active now)
			{
				Id: strPtr("lmm:planned_work:35099"),
				Alert: &gtfs.Alert{
					ActivePeriod: []*gtfs.TimeRange{
						{
							Start: uint64Ptr(uint64(nowSec - 1800)),
							End:   uint64Ptr(uint64(nowSec + 3600)),
						},
					},
					InformedEntity: []*gtfs.EntitySelector{
						{
							RouteId:     strPtr("A"),
							StopId:      strPtr("A40"),
							DirectionId: uint32Ptr(0), // Northbound
						},
					},
					HeaderText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("A Trains skip High St Northbound"), Language: strPtr("en")},
						},
					},
					DescriptionText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("Planned track maintenance at High St"), Language: strPtr("en")},
						},
					},
				},
			},
			// 2. Expired alert (ended in the past)
			{
				Id: strPtr("lmm:planned_work:expired_991"),
				Alert: &gtfs.Alert{
					ActivePeriod: []*gtfs.TimeRange{
						{
							Start: uint64Ptr(uint64(nowSec - 7200)),
							End:   uint64Ptr(uint64(nowSec - 3600)),
						},
					},
					InformedEntity: []*gtfs.EntitySelector{
						{
							RouteId: strPtr("A"),
							StopId:  strPtr("A40"),
						},
					},
					HeaderText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("Expired work"), Language: strPtr("en")},
						},
					},
				},
			},
			// 3. Far future alert (>14 days in future)
			{
				Id: strPtr("lmm:planned_work:future_far_992"),
				Alert: &gtfs.Alert{
					ActivePeriod: []*gtfs.TimeRange{
						{
							Start: uint64Ptr(uint64(nowSec + 15*86400)),
							End:   uint64Ptr(uint64(nowSec + 16*86400)),
						},
					},
					InformedEntity: []*gtfs.EntitySelector{
						{
							RouteId: strPtr("A"),
							StopId:  strPtr("A40"),
						},
					},
					HeaderText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("Future work >14 days"), Language: strPtr("en")},
						},
					},
				},
			},
			// 4. Upcoming alert (<24 hours in future)
			{
				Id: strPtr("lmm:planned_work:upcoming_soon_993"),
				Alert: &gtfs.Alert{
					ActivePeriod: []*gtfs.TimeRange{
						{
							Start: uint64Ptr(uint64(nowSec + 4*3600)),
							End:   uint64Ptr(uint64(nowSec + 12*3600)),
						},
					},
					InformedEntity: []*gtfs.EntitySelector{
						{
							RouteId: strPtr("A"),
							StopId:  strPtr("A40"),
						},
					},
					HeaderText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("Upcoming track inspection tonight"), Language: strPtr("en")},
						},
					},
				},
			},
			// 5. Line-wide service alert with no stop_id
			{
				Id: strPtr("mta_alert:delays_c_994"),
				Alert: &gtfs.Alert{
					InformedEntity: []*gtfs.EntitySelector{
						{
							RouteId: strPtr("C"),
						},
					},
					HeaderText: &gtfs.TranslatedString{
						Translation: []*gtfs.TranslatedString_Translation{
							{Text: strPtr("C Trains are running with delays"), Language: strPtr("en")},
						},
					},
				},
			},
		},
	}

	// 1. Verify parseFeedToAlerts
	alerts, ts := parseFeedToAlerts(feed, nowSec)
	if ts != nowSec {
		t.Errorf("expected timestamp %d, got %d", nowSec, ts)
	}

	alertMap := make(map[string]ServiceAlert)
	for _, al := range alerts {
		alertMap[al.ID] = al
	}

	// Expired alert must be pruned
	if _, ok := alertMap["lmm:planned_work:expired_991"]; ok {
		t.Errorf("expected expired alert to be pruned, but found in parsed alerts")
	}

	// Far-future (>14d) alert must be pruned
	if _, ok := alertMap["lmm:planned_work:future_far_992"]; ok {
		t.Errorf("expected >14d future alert to be pruned, but found in parsed alerts")
	}

	// Active planned work alert must be parsed
	pwAlert, ok := alertMap["lmm:planned_work:35099"]
	if !ok {
		t.Fatalf("expected lmm:planned_work:35099 to be parsed")
	}
	if pwAlert.AlertType != "PLANNED WORK" {
		t.Errorf("expected AlertType 'PLANNED WORK', got %q", pwAlert.AlertType)
	}
	if !containsStr(pwAlert.StopIDs, "A40N") {
		t.Errorf("expected directional stopID 'A40N' in StopIDs, got %+v", pwAlert.StopIDs)
	}
	if pwAlert.HeaderText != "A Trains skip High St Northbound" {
		t.Errorf("unexpected header text: %q", pwAlert.HeaderText)
	}

	// Upcoming (<24h) alert must be parsed
	if _, ok := alertMap["lmm:planned_work:upcoming_soon_993"]; !ok {
		t.Errorf("expected upcoming (<24h) alert to be parsed")
	}

	// Line-wide alert must be parsed
	lwAlert, ok := alertMap["mta_alert:delays_c_994"]
	if !ok {
		t.Errorf("expected line-wide alert to be parsed")
	} else if lwAlert.AlertType != "SERVICE ALERT" {
		t.Errorf("expected AlertType 'SERVICE ALERT', got %q", lwAlert.AlertType)
	}

	// 2. Verify projectStationAlerts for High St (A40)
	staticPath := filepath.Join("..", "..", "data", "mta_static.json")
	svc, err := NewService(staticPath)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	highSt, ok := svc.GetStation("A40")
	if !ok {
		t.Fatalf("station A40 not found")
	}

	allowedRoutes := map[string]bool{"A": true, "C": true}
	stationAlerts, northAlerts, southAlerts := svc.projectStationAlerts(highSt, allowedRoutes, alerts, nowSec)

	// Verify lmm:planned_work:35099 attaches to stationAlerts with Direction = "N"
	var foundInStation bool
	for _, al := range stationAlerts {
		if al.ID == "lmm:planned_work:35099" {
			foundInStation = true
			if al.Direction != "N" {
				t.Errorf("expected station alert copy to have Direction='N', got %q", al.Direction)
			}
		}
	}
	if !foundInStation {
		t.Errorf("expected lmm:planned_work:35099 in stationAlerts")
	}

	// Verify lmm:planned_work:35099 attaches to Northbound board
	var foundInNorth bool
	for _, al := range northAlerts {
		if al.ID == "lmm:planned_work:35099" {
			foundInNorth = true
		}
	}
	if !foundInNorth {
		t.Errorf("expected lmm:planned_work:35099 in northAlerts")
	}

	// Verify lmm:planned_work:35099 does NOT attach to Southbound board
	for _, al := range southAlerts {
		if al.ID == "lmm:planned_work:35099" {
			t.Errorf("expected lmm:planned_work:35099 NOT to be in southAlerts")
		}
	}

	// Verify upcoming (<24h) alert attaches to both North and South (no direction restriction)
	var foundUpcomingNorth, foundUpcomingSouth bool
	for _, al := range northAlerts {
		if al.ID == "lmm:planned_work:upcoming_soon_993" {
			foundUpcomingNorth = true
		}
	}
	for _, al := range southAlerts {
		if al.ID == "lmm:planned_work:upcoming_soon_993" {
			foundUpcomingSouth = true
		}
	}
	if !foundUpcomingNorth || !foundUpcomingSouth {
		t.Errorf("expected upcoming alert in both directions, got N=%v S=%v", foundUpcomingNorth, foundUpcomingSouth)
	}

	// Verify line-wide C train alert attaches to both directions
	var foundLWNorth, foundLWSouth bool
	for _, al := range northAlerts {
		if al.ID == "mta_alert:delays_c_994" {
			foundLWNorth = true
		}
	}
	for _, al := range southAlerts {
		if al.ID == "mta_alert:delays_c_994" {
			foundLWSouth = true
		}
	}
	if !foundLWNorth || !foundLWSouth {
		t.Errorf("expected line-wide alert in both directions, got N=%v S=%v", foundLWNorth, foundLWSouth)
	}

	// 3. Verify GetStationBoard attaches alerts and updates ServiceNote when direction has no trains
	// Store mock alerts into service snapshot
	svc.alertsSnapshot.Store(&SystemAlertsResponse{
		LastUpdated:   time.Now().UTC().Format(time.RFC3339),
		FeedTimestamp: nowSec,
		Alerts:        []ServiceAlert{pwAlert},
	})

	board, err := svc.GetStationBoard(context.Background(), "A40", "A", false)
	if err != nil {
		t.Fatalf("GetStationBoard failed: %v", err)
	}
	if len(board.Northbound.Alerts) == 0 {
		t.Errorf("expected board.Northbound.Alerts to contain planned work alert")
	}
	if len(board.Southbound.Alerts) != 0 {
		t.Errorf("expected board.Southbound.Alerts to be empty, got %d alerts", len(board.Southbound.Alerts))
	}
	// If Northbound has no trains (e.g. skipped due to work), verify ServiceNote displays alert header
	if len(board.Northbound.Trains) == 0 {
		if board.Northbound.ServiceNote != pwAlert.HeaderText {
			t.Errorf("expected Northbound.ServiceNote=%q, got %q", pwAlert.HeaderText, board.Northbound.ServiceNote)
		}
	}
}
