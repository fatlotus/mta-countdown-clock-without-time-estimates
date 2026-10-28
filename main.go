package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mta-countdown/internal/mta"
)

const defaultStationID = "127" // Times Sq-42 St (Manhattan - 1/2/3 + Complex 611)

func main() {
	port := flag.String("port", "8080", "HTTP server port")
	dataPath := flag.String("data", "data/mta_static.json", "Path to static MTA GTFS JSON")
	webDir := flag.String("web", "web", "Path to static web assets directory")
	flag.Parse()

	if envPort := os.Getenv("PORT"); envPort != "" {
		*port = envPort
	}

	svc, err := mta.NewService(*dataPath)
	if err != nil {
		log.Fatalf("Failed to initialize MTA service: %v", err)
	}

	mux := http.NewServeMux()

	// GET /api/stations -> returns all 496 stations + route metadata + default station ID (127 Times Sq-42 St)
	mux.HandleFunc("/api/stations", func(w http.ResponseWriter, r *http.Request) {
		stations, routes := svc.GetStations()
		writeJSON(w, http.StatusOK, map[string]any{
			"default_station_id": defaultStationID,
			"stations":           stations,
			"routes":             routes,
		})
	})

	// GET /api/trains -> returns system-wide snapshot of all active trains for offline caching
	mux.HandleFunc("/api/trains", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		trains, err := svc.GetAllTrains(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, trains)
	})

	// GET /api/board?station=A40&route=A&mode=auto|live|simulated
	mux.HandleFunc("/api/board", func(w http.ResponseWriter, r *http.Request) {
		stationID := r.URL.Query().Get("station")
		if stationID == "" {
			stationID = defaultStationID
		}
		focusRoute := r.URL.Query().Get("route")
		mode := r.URL.Query().Get("mode")
		forceSim := mode == "simulated"

		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		board, err := svc.GetStationBoard(ctx, stationID, focusRoute, forceSim)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, board)
	})

	// Serve frontend files
	absWeb, err := filepath.Abs(*webDir)
	if err != nil {
		log.Fatalf("Invalid web directory: %v", err)
	}

	// Service Worker must never be HTTP cached so clients receive updates immediately
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		http.ServeFile(w, r, filepath.Join(absWeb, "sw.js"))
	})

	fs := http.FileServer(http.Dir(absWeb))
	mux.Handle("/", fs)

	addr := ":" + *port
	fmt.Printf("MTA New Tech Countdown Clock running at http://localhost%s (Default station: Times Sq-42 St [%s])\n", addr, defaultStationID)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server exited: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
