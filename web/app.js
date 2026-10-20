(() => {
  const DEFAULT_STATION_ID = "127"; // Times Sq-42 St (Manhattan - 1/2/3 + Complex 611)
  const REFRESH_INTERVAL_MS = 10000;
  const FOCUS_WINDOW_RADIUS = 8; // ±8 stations before & after target station

  let allStations = [];
  let routeMap = {};
  let currentStationId = DEFAULT_STATION_ID;
  let currentFocusRoute = "";
  let currentView = "board"; // "board" | "trackmap"
  let selectedBorough = "ALL";
  let latestBoardData = null;
  let refreshTimer = null;

  // Sync theme with system prefers-color-scheme
  const colorSchemeMedia = window.matchMedia
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;
  function applySystemTheme() {
    const isDark = colorSchemeMedia ? colorSchemeMedia.matches : true;
    document.documentElement.setAttribute("data-theme", isDark ? "dark" : "light");
  }
  applySystemTheme();
  if (colorSchemeMedia && typeof colorSchemeMedia.addEventListener === "function") {
    colorSchemeMedia.addEventListener("change", applySystemTheme);
  }

  // DOM Elements
  const stationSearch = document.getElementById("station-search");
  const stationDropdown = document.getElementById("station-dropdown");
  const stationListEl = document.getElementById("station-list");
  const boroughTabs = document.getElementById("borough-tabs");
  const resetHighStBtn = document.getElementById("reset-high-st");

  const viewBoardBtn = document.getElementById("view-board-btn");
  const viewTrackmapBtn = document.getElementById("view-trackmap-btn");
  const boardViewContainer = document.getElementById("board-view-container");
  const trackmapViewContainer = document.getElementById("trackmap-view-container");

  const trackmapHeadingEl = document.getElementById("trackmap-heading");
  const trackmapColumnHeadersEl = document.getElementById("trackmap-column-headers");
  const trackmapDiagramBodyEl = document.getElementById("trackmap-diagram-body");

  const stationNameEl = document.getElementById("station-name");
  const stationBulletsEl = document.getElementById("station-bullets");
  const stationLineEl = document.getElementById("station-line");
  const stationBoroughEl = document.getElementById("station-borough");
  const stationIdBadgeEl = document.getElementById("station-id-badge");

  const lcdTimeEl = document.getElementById("lcd-time");
  const feedSourceBadgeEl = document.getElementById("feed-source-badge");
  const lastUpdatedTextEl = document.getElementById("last-updated-text");

  const northDirHeadingEl = document.getElementById("north-dir-heading");
  const northCountBadgeEl = document.getElementById("north-count-badge");
  const northTrainsEl = document.getElementById("north-trains");

  const southDirHeadingEl = document.getElementById("south-dir-heading");
  const southCountBadgeEl = document.getElementById("south-count-badge");
  const southTrainsEl = document.getElementById("south-trains");

  function getRouteMeta(routeId) {
    if (routeMap[routeId]) return routeMap[routeId];
    if (routeId === "S" && routeMap["GS"]) return routeMap["GS"];
    if (routeId === "SIR" && routeMap["SI"]) return routeMap["SI"];
    return {
      id: routeId,
      short_name: routeId,
      color: "#7C858C",
      text_color: "#FFFFFF",
    };
  }

  function createRouteBullet(routeId, sizeClass = "") {
    const meta = getRouteMeta(routeId);
    const span = document.createElement("span");
    span.className = `route-bullet ${sizeClass}`.trim();
    span.style.backgroundColor = meta.color || "#7C858C";
    span.style.color = meta.text_color || "#FFFFFF";
    span.textContent = meta.short_name || routeId;
    span.title = meta.long_name || `${routeId} Train`;
    return span;
  }

  function updateClock() {
    const now = new Date();
    lcdTimeEl.textContent = now.toLocaleTimeString("en-US", {
      hour12: false,
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }

  async function init() {
    const params = new URLSearchParams(window.location.search);
    const paramStation = params.get("station");
    const paramRoute = params.get("route");
    const paramView = params.get("view") || params.get("mode");
    const isSnapshot = params.get("snapshot") === "1";
    if (isSnapshot) {
      document.body.classList.add("snapshot-mode");
    }
    if (paramRoute) {
      currentFocusRoute = paramRoute;
    }
    if (paramView === "trackmap") {
      setViewMode("trackmap", false);
    }

    updateClock();
    if (!isSnapshot) {
      setInterval(updateClock, 1000);
    }

    try {
      const res = await fetch("/api/stations");
      const data = await res.json();
      allStations = data.stations || [];
      routeMap = data.routes || {};
      const defaultId = data.default_station_id || DEFAULT_STATION_ID;

      if (paramStation && allStations.some((s) => s.id === paramStation)) {
        currentStationId = paramStation;
      } else {
        currentStationId = defaultId;
      }

      renderStationDropdown();
      await loadStationBoard(currentStationId);
    } catch (err) {
      console.error("Failed to initialize stations:", err);
      lastUpdatedTextEl.textContent = "Connection error";
    }

    if (!isSnapshot) {
      refreshTimer = setInterval(() => {
        loadStationBoard(currentStationId, true);
      }, REFRESH_INTERVAL_MS);
    }
  }

  function setViewMode(view, updateUrl = true) {
    currentView = view;
    if (view === "trackmap") {
      viewTrackmapBtn.classList.add("active");
      viewBoardBtn.classList.remove("active");
      boardViewContainer.classList.add("hidden");
      trackmapViewContainer.classList.remove("hidden");
    } else {
      viewBoardBtn.classList.add("active");
      viewTrackmapBtn.classList.remove("active");
      trackmapViewContainer.classList.add("hidden");
      boardViewContainer.classList.remove("hidden");
    }
    if (updateUrl) {
      syncUrlParams();
    }
    if (latestBoardData) {
      renderTrackMap(latestBoardData);
    }
  }

  function syncUrlParams() {
    const url = new URL(window.location.href);
    if (currentStationId === DEFAULT_STATION_ID) {
      url.searchParams.delete("station");
    } else {
      url.searchParams.set("station", currentStationId);
    }
    if (currentFocusRoute) {
      url.searchParams.set("route", currentFocusRoute);
    } else {
      url.searchParams.delete("route");
    }
    if (currentView === "trackmap") {
      url.searchParams.set("view", "trackmap");
    } else {
      url.searchParams.delete("view");
    }
    window.history.replaceState({}, "", url.toString());
  }

  function renderStationDropdown() {
    const query = (stationSearch.value || "").trim().toLowerCase();
    stationListEl.innerHTML = "";

    const filtered = allStations.filter((st) => {
      if (selectedBorough !== "ALL" && st.borough !== selectedBorough) {
        return false;
      }
      if (!query) return true;
      const matchName = st.name.toLowerCase().includes(query);
      const matchLine = st.line.toLowerCase().includes(query);
      const matchId = st.id.toLowerCase().includes(query);
      const matchRoute = (st.routes || []).some((r) => r.toLowerCase() === query);
      return matchName || matchLine || matchId || matchRoute;
    });

    if (filtered.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-board-state";
      empty.innerHTML = `<div class="empty-title">No matching stations found</div>`;
      stationListEl.appendChild(empty);
      return;
    }

    const fragment = document.createDocumentFragment();
    filtered.forEach((st) => {
      const row = document.createElement("div");
      row.className = `station-option ${st.id === currentStationId ? "selected" : ""}`;
      row.setAttribute("role", "option");
      row.dataset.id = st.id;

      const left = document.createElement("div");
      left.className = "station-option-left";

      const nameEl = document.createElement("div");
      nameEl.className = "station-option-name";
      nameEl.textContent = st.name;

      const subEl = document.createElement("div");
      subEl.className = "station-option-sub";
      subEl.textContent = `${st.borough} • ${st.line} (#${st.id})`;

      left.appendChild(nameEl);
      left.appendChild(subEl);

      const bullets = document.createElement("div");
      bullets.className = "station-option-bullets";
      (st.routes || []).forEach((r) => {
        bullets.appendChild(createRouteBullet(r, "sm"));
      });

      row.appendChild(left);
      row.appendChild(bullets);

      row.addEventListener("click", () => {
        selectStation(st.id);
        stationDropdown.classList.add("hidden");
      });

      fragment.appendChild(row);
    });

    stationListEl.appendChild(fragment);
  }

  async function selectStation(stationId, focusRoute = "") {
    currentStationId = stationId;
    currentFocusRoute = focusRoute || "";
    syncUrlParams();
    await loadStationBoard(stationId);
  }

  async function loadStationBoard(stationId, isBackground = false) {
    if (!isBackground) {
      lastUpdatedTextEl.textContent = "Fetching train locations...";
    }
    try {
      const routeParam = currentFocusRoute
        ? `&route=${encodeURIComponent(currentFocusRoute)}`
        : "";
      const res = await fetch(
        `/api/board?station=${encodeURIComponent(stationId)}${routeParam}`
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const board = await res.json();
      if (board.route_metadata) {
        routeMap = { ...routeMap, ...board.route_metadata };
      }
      latestBoardData = board;
      renderBoard(board);
      renderTrackMap(board);
    } catch (err) {
      console.error("Failed to fetch station board:", err);
      lastUpdatedTextEl.textContent = "Feed retry pending...";
    }
  }

  function renderBoard(board) {
    const st = board.station;
    const activeRoutes = board.active_routes || st.routes || [];
    const transferLinks = board.transfer_links || [];
    const activeLink = transferLinks.find((tl) => tl.is_active);
    const activeLineName = (activeLink && activeLink.line_name) || st.line;
    document.title = `MTA Subway Live Location Board — ${st.name} (${activeRoutes.join("/")})`;

    stationNameEl.textContent = st.name;
    stationLineEl.textContent = `${activeLineName} Line (${activeRoutes.join(" · ")})`;
    stationBoroughEl.textContent = st.borough;
    stationIdBadgeEl.textContent = `GTFS #${st.id}`;

    stationBulletsEl.innerHTML = "";
    if (transferLinks.length > 1) {
      transferLinks.forEach((tl) => {
        const pill = document.createElement("button");
        pill.type = "button";
        pill.className = `corridor-switcher-pill ${tl.is_active ? "active" : "inactive"}`;
        pill.title = tl.is_active
          ? `Currently viewing ${tl.routes.join("/")} on ${tl.line_name} Line`
          : `Switch to ${tl.routes.join("/")} trains (${tl.line_name} Line)`;

        const bulletsWrap = document.createElement("span");
        bulletsWrap.className = "corridor-pill-bullets";
        (tl.routes || []).forEach((r) => {
          bulletsWrap.appendChild(createRouteBullet(r, "sm"));
        });
        pill.appendChild(bulletsWrap);

        const lbl = document.createElement("span");
        lbl.className = "corridor-pill-label";
        lbl.textContent = tl.line_name;
        pill.appendChild(lbl);

        pill.addEventListener("click", () => {
          selectStation(tl.station_id, tl.focus_route);
        });
        stationBulletsEl.appendChild(pill);
      });
    } else {
      (st.routes || []).forEach((r) => {
        stationBulletsEl.appendChild(createRouteBullet(r));
      });
    }

    const isSimulated = board.data_source === "SIMULATED_FALLBACK";
    if (isSimulated) {
      feedSourceBadgeEl.textContent = "SIMULATED TRAIN POSITIONS";
      feedSourceBadgeEl.classList.add("simulated");
    } else {
      feedSourceBadgeEl.textContent = "LIVE MTA GTFS-RT";
      feedSourceBadgeEl.classList.remove("simulated");
    }

    const updatedTime = new Date().toLocaleTimeString("en-US", {
      hour12: false,
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    lastUpdatedTextEl.textContent = `Updated ${updatedTime}`;

    renderDirectionPanel(
      board.northbound,
      northDirHeadingEl,
      northCountBadgeEl,
      northTrainsEl,
      st.name
    );
    renderDirectionPanel(
      board.southbound,
      southDirHeadingEl,
      southCountBadgeEl,
      southTrainsEl,
      st.name
    );
  }

  function renderDirectionPanel(dirBoard, headingEl, countBadgeEl, containerEl, targetStationName) {
    headingEl.textContent = dirBoard.label || "Terminal";
    const trains = dirBoard.trains || [];
    countBadgeEl.textContent =
      trains.length === 1 ? "1 Approaching Train" : `${trains.length} Approaching Trains`;

    containerEl.innerHTML = "";

    if (trains.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-board-state";
      const note =
        dirBoard.service_note ||
        "No trains currently approaching this platform.";
      empty.innerHTML = `
        <div class="empty-title">No Active Trains on This Track</div>
        <div class="empty-note">${escapeHtml(note)}</div>
      `;
      containerEl.appendChild(empty);
      return;
    }

    trains.forEach((tr) => {
      const row = document.createElement("div");
      let rowHighlight = "";
      if (tr.stops_away === 0 && tr.status_type === "AT_TARGET") {
        rowHighlight = "in-station";
      } else if (tr.stops_away === 0 || (tr.stops_away === 1 && tr.status_type === "APPROACHING")) {
        rowHighlight = "approaching-target";
      }
      row.className = `train-row ${rowHighlight}`.trim();

      const routeCell = document.createElement("div");
      routeCell.className = "train-route-cell";

      const bullet = document.createElement("span");
      bullet.className = "route-bullet lg";
      bullet.style.backgroundColor = tr.route_color || "#0062CF";
      bullet.style.color = tr.route_text_color || "#FFFFFF";
      bullet.textContent = tr.route_short_name || tr.route_id;

      const destGroup = document.createElement("div");
      destGroup.className = "train-dest-group";

      const destName = document.createElement("div");
      destName.className = "train-dest-name";
      destName.textContent = tr.destination;
      destName.title = tr.destination;

      const tripSub = document.createElement("div");
      tripSub.className = "train-trip-sub";
      tripSub.textContent = `Trip ${tr.trip_id}`;

      destGroup.appendChild(destName);
      destGroup.appendChild(tripSub);
      routeCell.appendChild(bullet);
      routeCell.appendChild(destGroup);

      const locCell = document.createElement("div");
      locCell.className = "train-location-cell";

      const statusBanner = document.createElement("div");
      statusBanner.className = "location-status-banner";

      const dot = document.createElement("span");
      dot.className = `status-indicator-dot ${tr.status_type || ""}`.trim();

      const statusText = document.createElement("span");
      statusText.textContent = tr.location_status;

      statusBanner.appendChild(dot);
      statusBanner.appendChild(statusText);
      locCell.appendChild(statusBanner);

      if (Array.isArray(tr.recent_path) && tr.recent_path.length > 0) {
        const strip = document.createElement("div");
        strip.className = "stop-strip";

        tr.recent_path.forEach((stopName, idx) => {
          if (idx > 0) {
            const arrow = document.createElement("span");
            arrow.className = "strip-arrow";
            arrow.textContent = "→";
            strip.appendChild(arrow);
          }
          const node = document.createElement("span");
          const isCurrent = idx === 0;
          const isTarget = idx === tr.recent_path.length - 1 && stopName === targetStationName;
          node.className = `strip-node ${isCurrent ? "current" : ""} ${isTarget ? "target" : ""}`.trim();
          node.textContent = stopName;
          strip.appendChild(node);
        });

        locCell.appendChild(strip);
      }

      const distCell = document.createElement("div");
      distCell.className = "train-distance-cell";

      const badge = document.createElement("span");
      let badgeVariant = "";
      if (tr.stops_away === 0) {
        badgeVariant = "now";
      } else if (tr.stops_away === 1) {
        badgeVariant = "next";
      }
      badge.className = `stops-away-badge ${badgeVariant}`.trim();
      badge.textContent = tr.stops_away_label;

      const microTag = document.createElement("span");
      microTag.className = "status-tag-micro";
      if (tr.status_type === "AT_TARGET") {
        microTag.textContent = "Boarding Now";
      } else if (tr.status_type === "BETWEEN") {
        microTag.textContent = "In Transit";
      } else if (tr.status_type === "APPROACHING") {
        microTag.textContent = "Approaching Stop";
      } else {
        microTag.textContent = "Stopped at Station";
      }

      distCell.appendChild(badge);
      distCell.appendChild(microTag);

      row.appendChild(routeCell);
      row.appendChild(locCell);
      row.appendChild(distCell);

      containerEl.appendChild(row);
    });
  }

  /* ==========================================================================
     TRACK MAP MODE RENDERER
     - Converges Local & Express rails into a single combined track where they
       share consecutive stations (e.g., Chambers St – High St on A/C).
     - Splits into parallel Local (outer) & Express (inner) tracks where Express
       bypasses local stations, with SVG switch curves at convergence stations.
     - Uses rectangular platform bars for stations so circular route bullets
       only appear on actual train cars.
     ========================================================================== */
  function createSvgLine(x1, y1, x2, y2, cls) {
    const ns = "http://www.w3.org/2000/svg";
    const path = document.createElementNS(ns, "path");
    if (x1 === x2) {
      path.setAttribute("d", `M ${x1} ${y1} L ${x2} ${y2}`);
    } else {
      const midY = (y1 + y2) / 2;
      path.setAttribute("d", `M ${x1} ${y1} C ${x1} ${midY}, ${x2} ${midY}, ${x2} ${y2}`);
    }
    path.setAttribute("class", cls);
    return path;
  }

  function renderTrackMap(board) {
    const tmap = board.track_map;
    if (!tmap || !Array.isArray(tmap.nodes) || tmap.nodes.length === 0) {
      return;
    }

    const st = board.station;
    const hasExp = Boolean(tmap.has_express_track);

    trackmapHeadingEl.textContent = `${tmap.line_name} Line Corridor — Centered on ${st.name}`;

    // Render 3-Column Header: [Southbound Tracks] | [Stations] | [Northbound Tracks]
    trackmapColumnHeadersEl.innerHTML = "";

    const buildCorridorHeader = (title, dirLabel, isSouth) => {
      const col = document.createElement("div");
      col.className = "track-col-head";
      const mainTitle = document.createElement("div");
      mainTitle.className = "track-col-title";
      mainTitle.textContent = title;
      col.appendChild(mainTitle);

      if (dirLabel) {
        const dirSub = document.createElement("div");
        dirSub.className = "track-col-dir-sub";
        dirSub.textContent = dirLabel;
        col.appendChild(dirSub);
      }

      if (hasExp) {
        const sub = document.createElement("div");
        sub.className = "track-col-sublabels";
        const locStr = `Local (${(tmap.local_routes || []).join("/")})`;
        const expStr = `Express (${(tmap.express_routes || []).join("/")})`;
        sub.innerHTML = isSouth
          ? `<span>${escapeHtml(locStr)}</span><span>${escapeHtml(expStr)}</span>`
          : `<span>${escapeHtml(expStr)}</span><span>${escapeHtml(locStr)}</span>`;
        col.appendChild(sub);
      }
      return col;
    };

    const centerHeader = document.createElement("div");
    centerHeader.className = "track-col-head";
    centerHeader.innerHTML = `
      <div class="track-col-title">Station Platforms (North → South)</div>
      <div class="track-col-sublabels">Click any station box to focus</div>
    `;

    trackmapColumnHeadersEl.appendChild(
      buildCorridorHeader("↓ Southbound", tmap.south_label, true)
    );
    trackmapColumnHeadersEl.appendChild(centerHeader);
    trackmapColumnHeadersEl.appendChild(
      buildCorridorHeader("↑ Northbound", tmap.north_label, false)
    );

    // Index Active Vehicles by (placement, node_index, direction)
    const vehicles = tmap.vehicles || [];
    const vehMap = new Map();
    const vKey = (placement, idx, dir) => `${placement}:${idx}:${dir}`;

    vehicles.forEach((v) => {
      const k = vKey(v.placement, v.node_index, v.direction);
      if (!vehMap.has(k)) vehMap.set(k, []);
      vehMap.get(k).push(v);
    });

    const targetIdx = tmap.target_index || 0;
    const startIdx = Math.max(0, targetIdx - FOCUS_WINDOW_RADIUS);
    const endIdx = Math.min(tmap.nodes.length - 1, targetIdx + FOCUS_WINDOW_RADIUS);

    trackmapDiagramBodyEl.innerHTML = "";
    const frag = document.createDocumentFragment();

    // Helper to build a single direction's track cell (either on a Station Row or an Inter-Station Segment Row)
    const buildDirectionCorridorCell = ({
      isStationRow,
      nodeIdx,
      dir,
      prevTopology, // "MERGED" | "SPLIT"
      nextTopology, // "MERGED" | "SPLIT"
      node,
    }) => {
      const cell = document.createElement("div");
      cell.className = "tmap-corridor-cell";

      // Horizontal X coordinates (0..100 SVG viewBox) for Local, Express, and Merged tracks:
      // Southbound (left column): Local on outer left (26), Express on inner right (74), Merged in center (50)
      // Northbound (right column): Express on inner left (26), Local on outer right (74), Merged in center (50)
      const localX = dir === "S" ? 26 : 74;
      const expressX = dir === "S" ? 74 : 26;
      const mergedX = 50;

      const ns = "http://www.w3.org/2000/svg";
      const svg = document.createElementNS(ns, "svg");
      svg.setAttribute("viewBox", "0 0 100 100");
      svg.setAttribute("preserveAspectRatio", "none");
      svg.setAttribute("class", "tmap-rail-svg");

      if (isStationRow) {
        // Station rows ALWAYS have straight vertical rails so the track never curves inside the station
        // or looks like an extra branch line!
        const centerIsSplit = hasExp && !Boolean(node.is_merged_track);

        if (centerIsSplit) {
          svg.appendChild(createSvgLine(localX, 0, localX, 100, "svg-rail-local"));
          svg.appendChild(createSvgLine(expressX, 0, expressX, 100, "svg-rail-express"));
        } else {
          svg.appendChild(createSvgLine(mergedX, 0, mergedX, 100, "svg-rail-merged"));
        }

        cell.appendChild(svg);

        // Rectangular Station Platform Stop Bar(s)
        if (centerIsSplit && node.has_express_bypass) {
          // Local-only station: platform bar sits ONLY on the Local track; Express rail bypasses cleanly!
          const localBar = document.createElement("div");
          localBar.className = "platform-stop-bar local-only-platform";
          localBar.style.left = `${localX}%`;
          localBar.title = `${node.name}: Local track stops here; Express track bypasses`;
          cell.appendChild(localBar);
        } else if (centerIsSplit && node.is_express_station) {
          // 4-Track Express Station (e.g. 14 St, 34 St, 42 St, 59 St, Canal St, Hoyt-Schermerhorn, Nostrand Av):
          // Separate Local & Express rails pass straight through the station, each with its own platform bar!
          const localBar = document.createElement("div");
          localBar.className = "platform-stop-bar local-only-platform";
          localBar.style.left = `${localX}%`;
          localBar.title = `${node.name}: Local Track Platform`;
          cell.appendChild(localBar);

          const expBar = document.createElement("div");
          expBar.className = "platform-stop-bar local-only-platform express-track-platform";
          expBar.style.left = `${expressX}%`;
          expBar.title = `${node.name}: Express Track Platform (separate track)`;
          cell.appendChild(expBar);
        } else {
          // Combined 2-track station (e.g. Chambers St, Fulton St, High St, Jay St-MetroTech)
          const platformBar = document.createElement("div");
          platformBar.className = "platform-stop-bar merged-platform";
          platformBar.style.left = "50%";
          platformBar.title = `${node.name}: Shared track platform`;
          cell.appendChild(platformBar);
        }
      } else {
        // Inter-station segment row (y: 0 -> 100) between upper station (prevTopology) and lower station (nextTopology)
        const topIsSplit = hasExp && prevTopology === "SPLIT";
        const botIsSplit = hasExp && nextTopology === "SPLIT";

        if (topIsSplit && botIsSplit) {
          svg.appendChild(createSvgLine(localX, 0, localX, 100, "svg-rail-local"));
          svg.appendChild(createSvgLine(expressX, 0, expressX, 100, "svg-rail-express"));
        } else if (!topIsSplit && !botIsSplit) {
          svg.appendChild(createSvgLine(mergedX, 0, mergedX, 100, "svg-rail-merged"));
        } else if (topIsSplit && !botIsSplit) {
          // 2 tracks smoothly merge into 1 track in the tunnel between stations!
          svg.appendChild(createSvgLine(localX, 0, mergedX, 100, "svg-rail-local"));
          svg.appendChild(createSvgLine(expressX, 0, mergedX, 100, "svg-rail-express"));
        } else {
          // 1 track smoothly splits into 2 tracks in the tunnel between stations!
          svg.appendChild(createSvgLine(mergedX, 0, localX, 100, "svg-rail-local"));
          svg.appendChild(createSvgLine(mergedX, 0, expressX, 100, "svg-rail-express"));
        }
        cell.appendChild(svg);
      }

      // Render Active Train Cars in this cell
      const placement = isStationRow ? "AT_STOP" : "BETWEEN_STOPS";
      const cellVehicles = vehMap.get(vKey(placement, nodeIdx, dir)) || [];
      if (cellVehicles.length > 0) {
        // Group vehicles by their horizontal track position (Merged 50%, Local localX%, Express expressX%)
        const isSplitHere = isStationRow
          ? hasExp && !Boolean(node && node.is_merged_track)
          : hasExp && prevTopology === "SPLIT" && nextTopology === "SPLIT";

        const slots = new Map();
        cellVehicles.forEach((v) => {
          let xPct = mergedX;
          if (isSplitHere) {
            xPct = v.track_type === "EXPRESS" ? expressX : localX;
          }
          if (!slots.has(xPct)) slots.set(xPct, []);
          slots.get(xPct).push(v);
        });

        slots.forEach((vList, xPct) => {
          const slotEl = document.createElement("div");
          slotEl.className = "track-slot";
          slotEl.style.left = `${xPct}%`;

          vList.forEach((v) => {
            const car = document.createElement("div");
            car.className = "train-car-token";
            car.title = `${v.route_short_name} Train to ${v.destination}\n${v.status_label}`;

            const b = document.createElement("span");
            b.className = "train-car-bullet";
            b.style.backgroundColor = v.route_color || "#0062CF";
            b.style.color = v.route_text_color || "#FFFFFF";
            b.textContent = v.route_short_name || v.route_id;

            const arrow = document.createElement("span");
            arrow.className = "train-car-arrow";
            arrow.textContent = v.direction === "N" ? "↑" : "↓";

            car.appendChild(b);
            car.appendChild(arrow);
            slotEl.appendChild(car);
          });

          cell.appendChild(slotEl);
        });
      }

      return cell;
    };

    for (let i = startIdx; i <= endIdx; i++) {
      const node = tmap.nodes[i];
      const prevTopology =
        i > 0
          ? tmap.nodes[i - 1].next_segment_tracks || "MERGED"
          : node.next_segment_tracks || "MERGED";
      const nextTopology = node.next_segment_tracks || "MERGED";

      const stRow = document.createElement("div");
      stRow.className = `tmap-row station-row ${node.is_target ? "target-focus" : ""}`.trim();
      stRow.dataset.stopId = node.stop_id;

      // Center Architectural Station Box (No circular route bullets so only trains have circles)
      const centerCol = document.createElement("div");
      centerCol.className = "tmap-station-center";

      const box = document.createElement("div");
      box.className = "station-platform-box";

      const boxTop = document.createElement("div");
      boxTop.className = "station-box-top";

      const stName = document.createElement("div");
      stName.className = "tmap-station-name";
      stName.textContent = node.name;
      boxTop.appendChild(stName);

      const offsetTag = document.createElement("span");
      offsetTag.className = `tmap-offset-tag ${node.is_target ? "here" : ""}`.trim();
      if (node.offset_from_focus === 0) {
        offsetTag.textContent = "YOU ARE HERE";
      } else if (node.offset_from_focus < 0) {
        offsetTag.textContent = `${Math.abs(node.offset_from_focus)} N`;
      } else {
        offsetTag.textContent = `+${node.offset_from_focus} S`;
      }
      boxTop.appendChild(offsetTag);
      box.appendChild(boxTop);

      const stTrackNote = document.createElement("div");
      stTrackNote.className = "tmap-station-track-note";
      const stopsList = (node.all_stop_routes || []).join(" · ");
      if (!hasExp) {
        stTrackNote.textContent = `Stops: ${stopsList}`;
      } else if (node.has_express_bypass) {
        stTrackNote.textContent = `Local Stop (${stopsList}) · Express Track Bypasses`;
      } else if (!node.is_merged_track && node.is_express_station) {
        stTrackNote.textContent = `Separate Local & Express Tracks (${stopsList})`;
      } else if (node.is_express_station) {
        stTrackNote.textContent = `Tracks Converge / Split (${stopsList})`;
      } else {
        stTrackNote.textContent = `Combined Single Track (${stopsList})`;
      }
      box.appendChild(stTrackNote);

      if (node.other_links && node.other_links.length > 0) {
        const linksRow = document.createElement("div");
        linksRow.className = "station-other-links-row";
        const linksLabel = document.createElement("span");
        linksLabel.className = "station-other-label";
        linksLabel.textContent = "Other lines ↗";
        linksRow.appendChild(linksLabel);

        node.other_links.forEach((link) => {
          (link.routes || []).forEach((r) => {
            const chip = document.createElement("button");
            chip.type = "button";
            chip.className = "station-transfer-chip";
            const meta = getRouteMeta(r);
            chip.style.setProperty("--chip-route-color", meta.color || "#4b5563");
            chip.title = `Switch to ${r} trains at ${node.name} (${link.line_name} Line)`;
            chip.textContent = meta.short_name || r;
            chip.addEventListener("click", (e) => {
              e.stopPropagation();
              selectStation(link.station_id, r);
            });
            linksRow.appendChild(chip);
          });
        });

        box.appendChild(linksRow);
      }

      centerCol.appendChild(box);

      stRow.appendChild(
        buildDirectionCorridorCell({
          isStationRow: true,
          nodeIdx: i,
          dir: "S",
          prevTopology,
          nextTopology,
          node,
        })
      );
      stRow.appendChild(centerCol);
      stRow.appendChild(
        buildDirectionCorridorCell({
          isStationRow: true,
          nodeIdx: i,
          dir: "N",
          prevTopology,
          nextTopology,
          node,
        })
      );

      stRow.addEventListener("click", () => {
        const stopRoutes = node.all_stop_routes || [];
        let nextFocusRoute = currentFocusRoute;
        if (!nextFocusRoute || !stopRoutes.includes(nextFocusRoute)) {
          nextFocusRoute = stopRoutes.length > 0 ? stopRoutes[0] : "";
        }
        if (node.stop_id !== currentStationId || nextFocusRoute !== currentFocusRoute) {
          selectStation(node.stop_id, nextFocusRoute);
        }
      });

      frag.appendChild(stRow);

      // Inter-station track segment row between station i and station i+1
      if (i < endIdx) {
        const nextNode = tmap.nodes[i + 1];
        const segTopTopology = node.is_merged_track ? "MERGED" : "SPLIT";
        const segBotTopology = nextNode.is_merged_track ? "MERGED" : "SPLIT";

        const segRow = document.createElement("div");
        segRow.className = "tmap-row segment-row";

        const segCenter = document.createElement("div");
        segCenter.className = "tmap-segment-center";
        if (hasExp && segTopTopology !== segBotTopology) {
          segCenter.textContent =
            segTopTopology === "SPLIT" ? "Tracks Merge" : "Tracks Split";
        } else if (hasExp && segTopTopology === "MERGED") {
          segCenter.textContent = "Shared Track";
        } else {
          segCenter.textContent = "";
        }

        segRow.appendChild(
          buildDirectionCorridorCell({
            isStationRow: false,
            nodeIdx: i,
            dir: "S",
            prevTopology: segTopTopology,
            nextTopology: segBotTopology,
            node: null,
          })
        );
        segRow.appendChild(segCenter);
        segRow.appendChild(
          buildDirectionCorridorCell({
            isStationRow: false,
            nodeIdx: i,
            dir: "N",
            prevTopology: segTopTopology,
            nextTopology: segBotTopology,
            node: null,
          })
        );

        frag.appendChild(segRow);
      }
    }

    trackmapDiagramBodyEl.appendChild(frag);
  }

  function escapeHtml(str) {
    return String(str)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // Event Listeners
  stationSearch.addEventListener("focus", () => {
    renderStationDropdown();
    stationDropdown.classList.remove("hidden");
  });

  stationSearch.addEventListener("input", () => {
    renderStationDropdown();
    stationDropdown.classList.remove("hidden");
  });

  document.addEventListener("click", (e) => {
    if (!stationDropdown.contains(e.target) && e.target !== stationSearch) {
      stationDropdown.classList.add("hidden");
    }
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== stationSearch) {
      e.preventDefault();
      stationSearch.focus();
      stationSearch.select();
    } else if (e.key === "Escape") {
      stationDropdown.classList.add("hidden");
      stationSearch.blur();
    }
  });

  boroughTabs.addEventListener("click", (e) => {
    const btn = e.target.closest(".borough-tab");
    if (!btn) return;
    selectedBorough = btn.dataset.borough || "ALL";
    boroughTabs.querySelectorAll(".borough-tab").forEach((b) => b.classList.remove("active"));
    btn.classList.add("active");
    renderStationDropdown();
  });

  resetHighStBtn.addEventListener("click", () => {
    stationSearch.value = "";
    selectStation(DEFAULT_STATION_ID);
  });

  viewBoardBtn.addEventListener("click", () => {
    setViewMode("board");
  });

  viewTrackmapBtn.addEventListener("click", () => {
    setViewMode("trackmap");
  });

  init();
})();
