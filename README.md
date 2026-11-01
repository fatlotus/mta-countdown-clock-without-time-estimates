# MTA Subway Countdown Clock (Without Time Estimates)

An MTA New Tech–inspired live subway countdown board and interactive track diagram for all **496 stations** in the New York City Subway system, built with **Go** and vanilla **JavaScript**.

Instead of displaying arrival minute predictions, this board tells riders **where each approaching train actually is right now** in the system:

- **At `<Station>`** (stopped at a station platform or boarding in station)
- **Approaching `<Station>`** (pulling into the platform)
- **Between `<Station A>` & `<Station B>`** (in transit between stops)
- **Stops away** count + live stop-by-stop progression strip toward the selected station

---

## Features

- **All 496 NYC Subway Stations**: Instant search by station name, line, route bullet, or GTFS stop ID, with borough filtering across Manhattan, Brooklyn, Queens, the Bronx, and Staten Island Railway (SIR). Defaults to **Times Sq–42 St** (`#127`).
- **Dual-Direction Station Board**: Northbound and Southbound LCD panels showing live train positions, trip IDs, recent stop progression strips, and stops-away badges.
- **Live Track & Platform Diagram (`Track Map` Mode)**:
  - Displays the `±8` stations before and after the focused stop along the active line corridor.
  - Renders **parallel Local and Express rails** where express tracks bypass local stops or pass through 4-track express stations (e.g., 14 St, 34 St–Penn Station, Hoyt–Schermerhorn), and **converged single rails** where local and express trains share the same track (e.g., Chambers St – High St – Jay St–MetroTech).
  - Visually distinguishes **rectangular station platform bars** from **circular MTA route bullets** (used exclusively for active train cars on the tracks).
- **Corridor Isolation & Complex Transfer Links**:
  - Filters both the Station Board and Track Map strictly to trains running on the active physical track corridor.
  - Provides deterministic corridor/line switcher buttons in the Vignelli station header and `OTHER LINES ↗` transfer chips on each station platform card in Track Map Mode (linking across multi-corridor stations and multi-platform MTA station complexes).
- **Offline-First & "Lie-Fi" Resilient**:
  - Precomputes all 496 stations, 532 station corridors, and 80 unique local/express track diagram spines once at server startup, caching 100% of station and track topology on the client (`localStorage`, Service Worker cache, and `/mta_catalog.js`).
  - Caches all active and upcoming 14-day MTA station service alerts and planned work notices (`/api/alerts`), evaluating `{ start, end }` active windows locally against calibrated monotonic time so planned reroutes published days in advance automatically surface at affected stations even when completely offline.
  - Renders station switches, corridor changes, and track maps synchronously in `0ms` by projecting the latest cached system-wide train and alert snapshots locally on the client.
  - Background syncs `/api/trains`, `/api/alerts`, and `/api/stations` behind strict `AbortController` timeouts (`2.5s` in the Service Worker and client) with JSON schema validation so slow or captive-portal subway Wi-Fi ("lie-fi") never blocks the UI, even when `navigator.onLine === true`.
- **Responsive & System Theme Aware**: Automatically follows the operating system's light/dark mode (`prefers-color-scheme`) and adapts cleanly from desktop monitors down to `390px` mobile screens.

---

## Project Structure

```text
.
├── data/
│   └── mta_static.json        # Static GTFS stations, complexes, routes, and stop sequences
├── internal/mta/
│   ├── types.go               # API response, topology, alert, and system train domain types
│   ├── topology.go            # Startup precomputation of corridors, transfers, and track spines
│   ├── topology_test.go       # Unit tests for precomputed station corridors and track spines
│   ├── alerts.go              # GTFS-RT subway alerts parser, 14-day window filter & station projection
│   ├── alerts_test.go         # Unit tests for directional and station service alert projection
│   ├── service.go             # Background GTFS-RT sync, unified train parser & board projection
│   ├── trackmap.go            # Vehicle-to-spine track projection for Track Map mode
│   └── service_test.go        # Unit tests for station board, track map, and corridor isolation
├── web/
│   ├── index.html             # App shell, Vignelli header sign, alerts section & board/diagram containers
│   ├── styles.css             # Responsive dark/light LCD, service alert & track diagram styling
│   ├── sw.js                  # Two-cache Service Worker with strict lie-fi timeouts
│   └── app.js                 # Offline-first client state, local board/alert/track projection & sync
├── main.go                    # HTTP server (/api/stations, /api/trains, /api/alerts, /api/board)
└── Dockerfile                 # Multi-stage Go build for Google Cloud Run
```

---

## Running Locally

```bash
go test ./...
go run . -port 8080
```

Then open `http://localhost:8080` in your browser.

---

## Deploying to Google Cloud Run

```bash
gcloud run deploy mta-countdown \
  --source . \
  --region=us-central1 \
  --max-instances=1 \
  --min-instances=0 \
  --allow-unauthenticated
```
