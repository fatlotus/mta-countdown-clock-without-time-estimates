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
- **Responsive & System Theme Aware**: Automatically follows the operating system's light/dark mode (`prefers-color-scheme`) and adapts cleanly from desktop monitors down to `390px` mobile screens.

---

## Project Structure

```text
.
├── data/
│   └── mta_static.json        # Static GTFS stations, complexes, routes, and stop sequences
├── internal/mta/
│   ├── types.go               # API response and domain types
│   ├── service.go             # GTFS-RT feed fetcher, cache, and station board builder
│   ├── trackmap.go            # Corridor grouping, local/express track topology & vehicle mapper
│   └── service_test.go        # Unit tests for station board, track map, and corridor isolation
├── web/
│   ├── index.html             # App shell, Vignelli header sign, and board/diagram containers
│   ├── styles.css             # Responsive dark/light LCD & track diagram styling
│   └── app.js                 # Frontend state, station search, board & SVG track renderer
├── main.go                    # HTTP server (/api/stations, /api/board, static assets)
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
