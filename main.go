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
	"strings"
	"time"

	"mta-countdown/internal/mta"
)

const defaultStationID = "127" // Times Sq-42 St (Manhattan - 1/2/3 + Complex 611)

var (
	gitCommit = os.Getenv("COMMIT_SHA")
	buildTime = time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
)

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
		static := svc.GetStaticData()
		writeJSON(w, http.StatusOK, map[string]any{
			"default_station_id":  defaultStationID,
			"stations":            static.Stations,
			"routes":              static.Routes,
			"canonical_sequences": static.CanonicalSequences,
			"daytime_sequences":   static.DaytimeSequences,
			"spines":              static.Spines,
			"station_corridors":   static.StationCorridors,
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

	// GET /api/version -> returns current running build metadata
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		commit := gitCommit
		if commit == "" {
			commit = "dev"
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"commit":     commit,
			"build_time": buildTime,
		})
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

	// Dynamically inject build metadata into app.js so cached Service Worker copies reflect exact build
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		appJsPath := filepath.Join(absWeb, "app.js")
		content, err := os.ReadFile(appJsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		commit := gitCommit
		if commit == "" {
			commit = "dev"
		}
		replaced := strings.ReplaceAll(string(content), "__BUILD_COMMIT__", commit)
		replaced = strings.ReplaceAll(replaced, "__BUILD_DATE__", buildTime)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(replaced))
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
