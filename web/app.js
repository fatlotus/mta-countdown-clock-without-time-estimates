(() => {
  const DEFAULT_STATION_ID = "127"; // Times Sq-42 St (Manhattan - 1/2/3 + Complex 611)
  const REFRESH_INTERVAL_MS = 10000;
  const FOCUS_WINDOW_RADIUS = 8; // ±8 stations before & after target station
  const STORAGE_KEY_STATION = "mta_rip_last_station";
  const STORAGE_KEY_ROUTE = "mta_rip_last_route";

  // Build Version injected at build / serve time into JavaScript
  const BUILD_VERSION = {
    commit: "__BUILD_COMMIT__",
    date: "__BUILD_DATE__",
  };

  function renderBuildInfo() {
    const buildInfoEl = document.getElementById("build-info");
    if (!buildInfoEl) return;
    const commit =
      BUILD_VERSION.commit && BUILD_VERSION.commit !== "__BUILD_COMMIT__"
        ? BUILD_VERSION.commit
        : "b5dafe1";
    const date =
      BUILD_VERSION.date && BUILD_VERSION.date !== "__BUILD_DATE__"
        ? BUILD_VERSION.date
        : "2026-10-28 22:10 UTC";

    const shortSha = commit.length > 7 ? commit.slice(0, 7) : commit;
    const commitUrl = `https://github.com/fatlotus/mta-dot-rip/commit/${commit}`;
    buildInfoEl.innerHTML = `Build: <a id="build-link" href="${commitUrl}" target="_blank" rel="noopener"><code id="build-commit">${shortSha}</code></a> • <span id="build-time">${date}</span>`;
  }

  function saveStickyStation(stationId, focusRoute = "") {
    try {
      if (stationId) {
        window.localStorage.setItem(STORAGE_KEY_STATION, stationId);
      }
      if (focusRoute) {
        window.localStorage.setItem(STORAGE_KEY_ROUTE, focusRoute);
      } else {
        window.localStorage.removeItem(STORAGE_KEY_ROUTE);
      }
    } catch (_) {
      // Ignore localStorage errors in private browsing modes
    }
  }

  function loadStickyStation() {
    try {
      return {
        stationId: window.localStorage.getItem(STORAGE_KEY_STATION) || "",
        focusRoute: window.localStorage.getItem(STORAGE_KEY_ROUTE) || "",
      };
    } catch (_) {
      return { stationId: "", focusRoute: "" };
    }
  }

  const STORAGE_KEY_STATIONS = "mta_rip_cached_stations";
  const STORAGE_KEY_TRAINS = "mta_rip_cached_trains";

  let allStations = [];
  let routeMap = {};
  let canonicalSequences = {};
  let daytimeSequences = {};
  let spinesMap = {};
  let stationCorridorsMap = {};
  const stationMap = {};
  let currentStationId = DEFAULT_STATION_ID;
  let currentFocusRoute = "";
  let currentView = "board"; // "board" | "trackmap"
  let selectedBorough = "ALL";
  let latestBoardData = null;
  let refreshTimer = null;

  // Offline & Staleness State (Subway Wi-Fi Resilience)
  let lastSuccessfulFetchPerf = 0;
  let lastSuccessfulFetchDate = null;
  let lastAppliedFeedTimestamp = 0;
  let isCurrentDataStale = false;
  let isCurrentDataExpired = false;
  let latestSystemTrains = null;
  let reconnectDebounceTimer = null;

  function applyStaticCatalog(data) {
    if (!data) return;
    if (Array.isArray(data.stations) && data.stations.length > 0) {
      allStations = data.stations;
      allStations.forEach((s) => {
        stationMap[s.id] = s;
      });
    }
    if (data.routes) routeMap = data.routes;
    if (data.canonical_sequences) canonicalSequences = data.canonical_sequences;
    if (data.daytime_sequences) daytimeSequences = data.daytime_sequences;
    if (data.spines) spinesMap = data.spines;
    if (data.station_corridors) stationCorridorsMap = data.station_corridors;
  }

  // Initialize station catalog synchronously from bundled static data
  function initStaticCatalog() {
    if (typeof window !== "undefined" && window.MTA_STATIC_DATA) {
      applyStaticCatalog(window.MTA_STATIC_DATA);
    }
    // If static data script wasn't loaded yet or lacked spines, overlay from localStorage
    if (allStations.length === 0 || Object.keys(spinesMap).length === 0) {
      try {
        const rawStations = window.localStorage.getItem(STORAGE_KEY_STATIONS);
        if (rawStations) {
          const parsed = JSON.parse(rawStations);
          applyStaticCatalog(parsed);
        }
      } catch (_) {}
    }
  }
  initStaticCatalog();

  try {
    const rawTrains = window.localStorage.getItem(STORAGE_KEY_TRAINS);
    if (rawTrains) {
      latestSystemTrains = JSON.parse(rawTrains);
      if (latestSystemTrains && typeof latestSystemTrains.feed_timestamp === "number") {
        lastAppliedFeedTimestamp = latestSystemTrains.feed_timestamp;
      }
    }
  } catch (_) {}

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

  const staleBannerEl = document.getElementById("stale-offline-banner");
  const staleBannerBadgeEl = document.getElementById("stale-banner-badge");
  const staleBannerTextEl = document.getElementById("stale-banner-text");

  function updateStaleBanner(show, badge = "", msg = "", isExpired = false) {
    if (!staleBannerEl) return;
    if (show) {
      staleBannerEl.classList.remove("hidden");
      if (isExpired) {
        staleBannerEl.classList.add("expired");
      } else {
        staleBannerEl.classList.remove("expired");
      }
      if (staleBannerBadgeEl) {
        staleBannerBadgeEl.textContent = badge || "⚠️ STALE DATA (>30s)";
      }
      if (staleBannerTextEl) {
        staleBannerTextEl.textContent =
          msg || "Showing cached train locations from subway Wi-Fi snapshot. Live tracking resumes at next station.";
      }
    } else {
      staleBannerEl.classList.add("hidden");
    }
  }

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

  function init() {
    const params = new URLSearchParams(window.location.search);
    const paramStation = params.get("station");
    const paramRoute = params.get("route");
    const paramView = params.get("view") || params.get("mode");
    const isSnapshot = params.get("snapshot") === "1";
    const sticky = loadStickyStation();

    if (isSnapshot) {
      document.body.classList.add("snapshot-mode");
    }
    if (paramRoute) {
      currentFocusRoute = paramRoute;
    } else if (!paramStation && !isSnapshot && sticky.focusRoute) {
      currentFocusRoute = sticky.focusRoute;
    }
    if (paramView === "trackmap") {
      setViewMode("trackmap", false);
    }

    if (paramStation && allStations.some((s) => s.id === paramStation)) {
      currentStationId = paramStation;
      if (!isSnapshot) {
        saveStickyStation(currentStationId, currentFocusRoute);
      }
    } else if (
      !paramStation &&
      !isSnapshot &&
      sticky.stationId &&
      allStations.some((s) => s.id === sticky.stationId)
    ) {
      currentStationId = sticky.stationId;
      syncUrlParams();
    } else {
      const defaultId = (allStations[0] && allStations[0].id) || DEFAULT_STATION_ID;
      currentStationId = defaultId;
    }

    updateClock();
    renderBuildInfo();

    // Render dropdown and station board IMMEDIATELY in 0ms synchronously
    if (allStations.length > 0) {
      renderStationDropdown();
    }
    renderCurrentStation();

    if (!isSnapshot) {
      setInterval(() => {
        updateClock();
        updateStalenessTicker();
      }, 1000);
      registerServiceWorker();
      setupReconnectListeners();

      // Non-blocking background sync with strict lie-fi timeouts
      syncTrains();
      syncCatalog();
      refreshTimer = setInterval(syncTrains, REFRESH_INTERVAL_MS);
    } else if (!latestSystemTrains) {
      syncTrains();
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
    if (currentStationId) {
      url.searchParams.set("station", currentStationId);
    } else {
      url.searchParams.delete("station");
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

  function normalizeSearchTerm(str) {
    return (str || "")
      .toLowerCase()
      .replace(/\b(\d+)(st|nd|rd|th)\b/g, "$1") // "14th" -> "14", "42nd" -> "42"
      .replace(/\bstreet\b/g, "st")
      .replace(/\bavenue\b/g, "av")
      .replace(/\bsquare\b/g, "sq")
      .replace(/[^a-z0-9]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
  }

  function renderStationDropdown() {
    const rawQuery = (stationSearch.value || "").trim().toLowerCase();
    const normQuery = normalizeSearchTerm(rawQuery);
    stationListEl.innerHTML = "";

    const filtered = allStations.filter((st) => {
      if (selectedBorough !== "ALL" && st.borough !== selectedBorough) {
        return false;
      }
      if (!rawQuery) return true;
      const matchNameNorm = normalizeSearchTerm(st.name).includes(normQuery);
      const matchNameRaw = st.name.toLowerCase().includes(rawQuery);
      const matchLineNorm = normalizeSearchTerm(st.line).includes(normQuery);
      const matchId = st.id.toLowerCase().includes(rawQuery);
      const matchRoute = (st.routes || []).some((r) => r.toLowerCase() === rawQuery);
      return matchNameNorm || matchNameRaw || matchLineNorm || matchId || matchRoute;
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

  function selectStation(stationId, focusRoute = "") {
    currentStationId = stationId;
    currentFocusRoute = focusRoute || "";
    saveStickyStation(currentStationId, currentFocusRoute);
    syncUrlParams();
    renderCurrentStation();
  }

  const DEFAULT_EXPRESS_ROUTES = new Set([
    "A", "B", "D", "E", "N", "Q",
    "2", "3", "4", "5", "6X", "7X", "FX", "Z"
  ]);

  function getCleanRouteId(r) {
    if (r === "SIR") return "SI";
    if (r === "S") return "GS";
    return r;
  }

  function getRouteSequence(r, stId) {
    const nr = getCleanRouteId(r);
    let s = daytimeSequences[nr + "_S"] || canonicalSequences[nr + "_S"];
    if (!s || !s.includes(stId)) {
      let rev = canonicalSequences[nr + "_N"];
      if (rev && rev.includes(stId)) s = [...rev].reverse();
    }
    return s || [];
  }

  function getStationTopology(st, focusRoute = "") {
    if (!st) return { trackMap: null, corridor: null };
    const corridors = stationCorridorsMap[st.id] || [];
    let selected = null;
    if (corridors.length > 0) {
      if (focusRoute) {
        selected = corridors.find(
          (sc) => sc.focus_route === focusRoute || (sc.active_routes || []).includes(focusRoute)
        );
      }
      if (!selected) {
        selected = corridors[0];
      }
    }

    let spine = selected ? spinesMap[selected.spine_id] : null;
    let trackMap = null;

    if (selected && spine && Array.isArray(spine.nodes)) {
      const nodes = spine.nodes.map((node, idx) => ({
        ...node,
        is_target: node.stop_id === st.id,
        offset_from_focus: idx - selected.target_index,
        other_links: node.other_links ? node.other_links.map((tl) => ({ ...tl })) : [],
      }));

      const transferLinks = (selected.transfer_links || []).map((tl) => ({
        ...tl,
        is_active: tl.station_id === st.id && tl.focus_route === selected.focus_route,
      }));

      trackMap = {
        line_name: selected.line_name || spine.line_name || st.line,
        has_express_track: Boolean(spine.has_express_track),
        local_routes: spine.local_routes || [],
        express_routes: spine.express_routes || [],
        corridor_routes: spine.corridor_routes || selected.trunk_routes || selected.active_routes || [],
        north_label: st.north_label || "Northbound",
        south_label: st.south_label || "Southbound",
        target_index: selected.target_index,
        nodes: nodes,
        vehicles: [],
      };

      return {
        trackMap,
        corridor: {
          ...selected,
          transfer_links: transferLinks,
        },
      };
    }

    // Compact fallback if precomputed spine is missing
    const route = focusRoute || (st.routes && st.routes[0]) || "1";
    const seq = getRouteSequence(route, st.id);
    const targetIdx = seq.indexOf(st.id) !== -1 ? seq.indexOf(st.id) : 0;
    const nodes = (seq.length > 0 ? seq : [st.id]).map((sid, idx) => {
      const sInfo = stationMap[sid] || { id: sid, name: sid, borough: st.borough, routes: [route] };
      return {
        stop_id: sid,
        name: sInfo.name,
        borough: sInfo.borough,
        is_target: sid === st.id,
        offset_from_focus: idx - targetIdx,
        local_routes: [route],
        express_routes: [],
        all_stop_routes: [route],
        is_express_station: false,
        has_express_bypass: false,
        is_merged_track: true,
        next_segment_tracks: "MERGED",
        other_links: [],
      };
    });

    trackMap = {
      line_name: st.line,
      has_express_track: false,
      local_routes: [route],
      express_routes: [],
      corridor_routes: [route],
      north_label: st.north_label || "Northbound",
      south_label: st.south_label || "Southbound",
      target_index: targetIdx,
      nodes: nodes,
      vehicles: [],
    };

    const dummyCorridor = {
      focus_route: route,
      line_name: st.line,
      active_routes: st.routes || [route],
      trunk_routes: [route],
      spine_id: "",
      target_index: targetIdx,
      transfer_links: [
        {
          station_id: st.id,
          line_name: st.line,
          focus_route: route,
          routes: st.routes || [route],
          is_active: true,
        },
      ],
    };

    return { trackMap, corridor: dummyCorridor };
  }

  function projectStationBoard(stationId, focusRoute = "") {
    const st = stationMap[stationId] || allStations.find((s) => s.id === stationId) || {
      id: stationId,
      name: stationId,
      borough: "Manhattan",
      routes: focusRoute ? [focusRoute] : [],
    };

    const { trackMap, corridor } = getStationTopology(st, focusRoute);
    const activeRoutes = (corridor && corridor.active_routes) || st.routes || [];
    const transferLinks = (corridor && corridor.transfer_links) || [];

    const northLabel = st.north_label || "Northbound / Terminal";
    const southLabel = st.south_label || "Southbound / Terminal";

    const dataSource =
      lastSuccessfulFetchPerf > 0
        ? (latestSystemTrains && latestSystemTrains.data_source) || "LIVE_MTA_GTFS_RT"
        : "OFFLINE_SYSTEM_SNAPSHOT";

    const board = {
      station: st,
      active_route: focusRoute,
      active_routes: activeRoutes,
      transfer_links: transferLinks,
      route_metadata: routeMap,
      northbound: {
        direction: "N",
        label: northLabel,
        trains: [],
        service_note: "",
      },
      southbound: {
        direction: "S",
        label: southLabel,
        trains: [],
        service_note: "",
      },
      track_map: trackMap,
      data_source: dataSource,
      feed_timestamp: (latestSystemTrains && latestSystemTrains.feed_timestamp) || 0,
      last_updated: new Date().toISOString(),
    };

    if (!latestSystemTrains || !Array.isArray(latestSystemTrains.trains)) {
      board.data_source = "OFFLINE_NO_TRAINS";
      board.northbound.service_note = "No train location data available offline.";
      board.southbound.service_note = "No train location data available offline.";
      return board;
    }

    const trains = latestSystemTrains.trains;
    const candidateRoutes =
      corridor && Array.isArray(corridor.trunk_routes) && corridor.trunk_routes.length > 0
        ? corridor.trunk_routes
        : activeRoutes;
    const allowedRoutesSet = new Set(candidateRoutes);
    const isExpressSet = new Set(trackMap ? trackMap.express_routes || [] : []);

    // 1. Project dual-direction arrivals for st
    const northTrains = [];
    const southTrains = [];

    trains.forEach((tr) => {
      const routeClean = getCleanRouteId(tr.route_id);
      if (allowedRoutesSet.size > 0 && !allowedRoutesSet.has(tr.route_id) && !allowedRoutesSet.has(routeClean)) {
        return;
      }

      const upcomingStops = tr.upcoming_stops || [];
      let targetIdx = -1;
      for (let i = 0; i < upcomingStops.length; i++) {
        if (upcomingStops[i].stop_id === st.id) {
          targetIdx = i;
          break;
        }
      }
      if (targetIdx === -1 && tr.upcoming_stop_ids) {
        targetIdx = tr.upcoming_stop_ids.indexOf(st.id);
      }
      if (targetIdx === -1 && tr.current_stop_id === st.id) {
        targetIdx = 0;
      }
      if (targetIdx === -1) {
        return;
      }

      const targetStop = upcomingStops[targetIdx] || { stop_id: st.id, track: tr.track };
      const stopsAway = targetIdx;
      let statusType = "";
      let locationStatus = "";
      let stopsAwayLabel = "";

      if (stopsAway === 0) {
        if (tr.status_type === "AT_STATION") {
          locationStatus = `At ${st.name} (Boarding in Station)`;
          statusType = "AT_TARGET";
          stopsAwayLabel = "In Station";
        } else {
          locationStatus = `Approaching ${st.name}`;
          statusType = "APPROACHING";
          stopsAwayLabel = "Entering Station";
        }
      } else {
        statusType = tr.status_type || "APPROACHING";
        locationStatus = tr.location_status || `Approaching ${tr.current_stop || ""}`;
        if (stopsAway === 1) {
          stopsAwayLabel = "1 stop away";
        } else {
          stopsAwayLabel = `${stopsAway} stops away`;
        }
      }

      const recentPath = [];
      const recentStopIDs = [];
      const startIdx = targetIdx > 4 ? targetIdx - 4 : 0;
      if (startIdx > 0) {
        recentPath.push(tr.current_stop || tr.current_stop_id, "...");
        recentStopIDs.push(tr.current_stop_id, "");
      }
      if (upcomingStops.length > 0) {
        for (let k = startIdx; k <= targetIdx; k++) {
          const sid = upcomingStops[k].stop_id;
          const sInfo = stationMap[sid];
          const sname = (sInfo && sInfo.name) || sid;
          recentPath.push(sname);
          recentStopIDs.push(sid);
        }
      } else {
        if (tr.current_stop) {
          recentPath.push(tr.current_stop);
          recentStopIDs.push(tr.current_stop_id);
        }
        if (tr.current_stop_id !== st.id) {
          recentPath.push(st.name);
          recentStopIDs.push(st.id);
        }
      }

      const arrTrack = targetStop.track || tr.track || "";
      let targetTime = targetStop.arrival_time;
      if (!targetTime) {
        const nowSec = Math.floor(Date.now() / 1000);
        targetTime = nowSec + stopsAway * 90;
      }

      const meta = getRouteMeta(tr.route_id);
      const arr = {
        trip_id: tr.trip_id,
        route_id: tr.route_id,
        route_short_name: tr.route_short_name || tr.route_id,
        route_color: meta.color || tr.route_color || "#7C858C",
        route_text_color: meta.text_color || tr.route_text_color || "#FFFFFF",
        direction: tr.direction,
        destination: tr.destination,
        destination_stop_id: tr.destination_stop_id,
        location_status: locationStatus,
        status_type: statusType,
        current_stop_id: tr.current_stop_id,
        current_stop: tr.current_stop,
        previous_stop_id: tr.previous_stop_id,
        previous_stop: tr.previous_stop,
        stops_away: stopsAway,
        stops_away_label: stopsAwayLabel,
        track: arrTrack,
        recent_path: recentPath,
        recent_stop_ids: recentStopIDs,
        sort_order: targetTime,
      };

      if (tr.direction === "N") {
        northTrains.push(arr);
      } else {
        southTrains.push(arr);
      }
    });

    northTrains.sort((a, b) => {
      if (a.stops_away !== b.stops_away) return a.stops_away - b.stops_away;
      return a.sort_order - b.sort_order;
    });
    southTrains.sort((a, b) => {
      if (a.stops_away !== b.stops_away) return a.stops_away - b.stops_away;
      return a.sort_order - b.sort_order;
    });

    board.northbound.trains = northTrains.slice(0, 8);
    board.southbound.trains = southTrains.slice(0, 8);

    if (board.northbound.trains.length === 0 && st.north_label) {
      board.northbound.service_note =
        "No active trains currently scheduled for this platform in MTA snapshot.";
    }
    if (board.southbound.trains.length === 0 && st.south_label) {
      board.southbound.service_note =
        "No active trains currently scheduled for this platform in MTA snapshot.";
    }

    // 2. Project track_map vehicles
    if (trackMap && Array.isArray(trackMap.nodes) && trackMap.nodes.length > 0) {
      const spineIdxMap = new Map();
      trackMap.nodes.forEach((n, idx) => {
        spineIdxMap.set(n.stop_id, idx);
      });

      const corridorRoutesSet = new Set(trackMap.corridor_routes || activeRoutes);
      const vehicles = [];

      trains.forEach((tr) => {
        const routeClean = getCleanRouteId(tr.route_id);
        if (!corridorRoutesSet.has(tr.route_id) && !corridorRoutesSet.has(routeClean)) {
          return;
        }
        const currStopID = tr.current_stop_id;
        if (!spineIdxMap.has(currStopID)) {
          return;
        }
        const nodeIdx = spineIdxMap.get(currStopID);

        let trackType = tr.track_type || "";
        if (!trackType) {
          if (trackMap.has_express_track && (isExpressSet.has(tr.route_id) || DEFAULT_EXPRESS_ROUTES.has(tr.route_id))) {
            trackType = "EXPRESS";
          } else {
            trackType = "LOCAL";
          }
        }

        let placement = "AT_STOP";
        let diagramNodeIdx = nodeIdx;
        let fromStopID = currStopID;
        let toStopID = currStopID;
        const currStopName = tr.current_stop || currStopID;
        let statusLabel = `At ${currStopName}`;

        const isAtStation =
          tr.status_type === "AT_STATION" ||
          (typeof tr.arr_diff === "number" &&
            typeof tr.dep_diff === "number" &&
            tr.arr_diff <= 20 &&
            tr.dep_diff >= -25);

        if (isAtStation) {
          placement = "AT_STOP";
          diagramNodeIdx = nodeIdx;
          statusLabel = `Stopped at ${currStopName}`;
          if (trackMap.nodes[diagramNodeIdx].is_merged_track) {
            trackType = "MERGED";
          } else if (trackMap.nodes[diagramNodeIdx].has_express_bypass) {
            trackType = "LOCAL";
          }
        } else {
          placement = "BETWEEN_STOPS";
          statusLabel = `En route to ${currStopName}`;

          if (tr.direction === "S") {
            if (nodeIdx > 0) {
              diagramNodeIdx = nodeIdx - 1;
              fromStopID = trackMap.nodes[diagramNodeIdx].stop_id;
              toStopID = currStopID;
            } else {
              placement = "AT_STOP";
              diagramNodeIdx = 0;
            }
          } else {
            if (nodeIdx < trackMap.nodes.length - 1) {
              diagramNodeIdx = nodeIdx;
              fromStopID = trackMap.nodes[nodeIdx + 1].stop_id;
              toStopID = currStopID;
            } else {
              placement = "AT_STOP";
              diagramNodeIdx = nodeIdx;
            }
          }

          if (diagramNodeIdx >= 0 && diagramNodeIdx < trackMap.nodes.length) {
            if (trackMap.nodes[diagramNodeIdx].next_segment_tracks === "MERGED") {
              trackType = "MERGED";
            }
          }
        }

        const meta = getRouteMeta(tr.route_id);
        vehicles.push({
          trip_id: tr.trip_id,
          route_id: tr.route_id,
          route_short_name: tr.route_short_name || tr.route_id,
          route_color: meta.color || tr.route_color || "#7C858C",
          route_text_color: meta.text_color || tr.route_text_color || "#FFFFFF",
          direction: tr.direction,
          track_type: trackType,
          track: tr.track || "",
          placement: placement,
          stop_id: currStopID,
          from_stop_id: fromStopID,
          to_stop_id: toStopID,
          node_index: diagramNodeIdx,
          status_label: statusLabel,
          destination: tr.destination || "",
        });
      });

      trackMap.vehicles = vehicles;
    }

    return board;
  }

  function renderCurrentStation() {
    latestBoardData = projectStationBoard(currentStationId, currentFocusRoute);
    if (latestBoardData) {
      renderBoard(latestBoardData);
      renderTrackMap(latestBoardData);
      updateStalenessTicker();
    }
  }

  async function fetchJsonWithTimeout(url, timeoutMs = 2500) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const res = await fetch(url, { signal: controller.signal, cache: "no-store" });
      if (!res || !res.ok || res.redirected) return null;
      const ct = (res.headers.get("content-type") || "").toLowerCase();
      if (!ct.includes("application/json")) return null;
      return await res.json();
    } catch (_) {
      return null;
    } finally {
      clearTimeout(timer);
    }
  }

  let isSyncingTrains = false;
  async function syncTrains() {
    if (isSyncingTrains) return;
    isSyncingTrains = true;
    try {
      const data = await fetchJsonWithTimeout("/api/trains", 2500);
      if (
        data &&
        !data.offline &&
        Array.isArray(data.trains) &&
        typeof data.feed_timestamp === "number"
      ) {
        const isNewer = data.feed_timestamp >= lastAppliedFeedTimestamp;
        const isCachedExpired = Math.abs(Date.now() / 1000 - lastAppliedFeedTimestamp) > 60;
        if (isNewer || isCachedExpired) {
          lastAppliedFeedTimestamp = data.feed_timestamp;
          latestSystemTrains = data;
          lastSuccessfulFetchPerf = performance.now();
          lastSuccessfulFetchDate = new Date();
          try {
            window.localStorage.setItem(STORAGE_KEY_TRAINS, JSON.stringify(data));
          } catch (_) {}
          renderCurrentStation();
        }
      }
    } catch (err) {
      console.warn("Background syncTrains error:", err);
    } finally {
      isSyncingTrains = false;
    }
  }

  let isSyncingCatalog = false;
  async function syncCatalog() {
    if (isSyncingCatalog) return;
    isSyncingCatalog = true;
    try {
      const data = await fetchJsonWithTimeout("/api/stations", 3000);
      if (data && Array.isArray(data.stations) && data.stations.length > 0) {
        applyStaticCatalog(data);
        try {
          window.localStorage.setItem(STORAGE_KEY_STATIONS, JSON.stringify(data));
        } catch (_) {}
        renderStationDropdown();
        renderCurrentStation();
      }
    } catch (err) {
      console.warn("Background syncCatalog error:", err);
    } finally {
      isSyncingCatalog = false;
    }
  }

  function updateStalenessTicker() {
    let ageSeconds = 0;
    if (lastSuccessfulFetchPerf > 0) {
      ageSeconds = Math.max(0, Math.round((performance.now() - lastSuccessfulFetchPerf) / 1000));
    } else if (latestBoardData && latestBoardData.feed_timestamp) {
      ageSeconds = Math.max(0, Math.round(Date.now() / 1000 - latestBoardData.feed_timestamp));
    }

    const isOffline = typeof navigator !== "undefined" && navigator.onLine === false;
    const isSnapshotSource =
      latestBoardData &&
      (latestBoardData.data_source === "OFFLINE_SYSTEM_SNAPSHOT" ||
        latestBoardData.data_source === "OFFLINE_NO_TRAINS");

    if (ageSeconds > 30 || isOffline || isSnapshotSource) {
      if (!isCurrentDataStale) {
        isCurrentDataStale = true;
        boardViewContainer.classList.add("stale");
        trackmapViewContainer.classList.add("stale");
        document.body.classList.add("stale");
        feedSourceBadgeEl.className = "feed-badge stale";
        feedSourceBadgeEl.textContent = isSnapshotSource ? "OFFLINE SNAPSHOT" : "STALE DATA (>30s)";
      }
      lastUpdatedTextEl.textContent = isOffline
        ? "Offline (Subway Wi-Fi Disconnected)"
        : `Updated ${ageSeconds > 0 ? ageSeconds + "s ago" : "from cache"} (Stale)`;

      if (ageSeconds > 90 || (isSnapshotSource && ageSeconds > 60)) {
        if (!isCurrentDataExpired) {
          isCurrentDataExpired = true;
          boardViewContainer.classList.add("expired");
          trackmapViewContainer.classList.add("expired");
          document.body.classList.add("expired");
          feedSourceBadgeEl.className = "feed-badge expired";
          feedSourceBadgeEl.textContent = "HISTORICAL SNAPSHOT";
        }
        updateStaleBanner(
          true,
          "⏱️ HISTORICAL DATA (>1m)",
          `Data is ${ageSeconds > 0 ? ageSeconds + "s" : ""} old. Train arrival times and positions may no longer be accurate.`,
          true
        );
        // Qualify in-station train badges with historical timestamp to prevent commuters from rushing
        document.querySelectorAll(".train-row").forEach((row) => {
          const badge = row.querySelector(".stops-away-badge.now");
          if (badge && !row.dataset.expiredAdjusted) {
            row.dataset.expiredAdjusted = "true";
            row.classList.add("expired-train");
            const m = Math.max(1, Math.floor(ageSeconds / 60));
            badge.textContent = `Last seen in station (${m}m ago)`;
            badge.title =
              "This train likely departed; snapshot is older than typical station dwell time.";
          }
        });
      } else {
        updateStaleBanner(
          true,
          "⚠️ STALE DATA (>30s)",
          `Viewing cached train locations from subway Wi-Fi snapshot (${ageSeconds}s old). Live tracking resumes at next station platform.`,
          false
        );
      }
    } else {
      if (isCurrentDataStale) {
        isCurrentDataStale = false;
        isCurrentDataExpired = false;
        boardViewContainer.classList.remove("stale", "expired");
        trackmapViewContainer.classList.remove("stale", "expired");
        document.body.classList.remove("stale", "expired");
        feedSourceBadgeEl.classList.remove("stale", "expired");
        updateStaleBanner(false);
        const isSimulated =
          latestBoardData && latestBoardData.data_source === "SIMULATED_FALLBACK";
        if (isSimulated) {
          feedSourceBadgeEl.textContent = "SIMULATED TRAIN POSITIONS";
          feedSourceBadgeEl.className = "feed-badge simulated";
        } else {
          feedSourceBadgeEl.textContent = "LIVE MTA GTFS-RT";
          feedSourceBadgeEl.className = "feed-badge";
        }
      }
      if (lastSuccessfulFetchDate) {
        const timeStr = lastSuccessfulFetchDate.toLocaleTimeString("en-US", {
          hour12: false,
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
        });
        lastUpdatedTextEl.textContent = `Updated ${timeStr}`;
      }
    }
  }

  function setupReconnectListeners() {
    const handleReconnect = () => {
      clearTimeout(reconnectDebounceTimer);
      // Debounce 350ms to allow station Wi-Fi DHCP handshake to stabilize
      reconnectDebounceTimer = setTimeout(() => {
        updateStalenessTicker();
        syncTrains();
      }, 350);
    };

    window.addEventListener("online", handleReconnect);
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        updateStalenessTicker();
        syncTrains();
      }
    });
  }

  function registerServiceWorker() {
    if (!("serviceWorker" in navigator)) return;

    const updateToast = document.getElementById("sw-update-toast");
    const updateBtn = document.getElementById("sw-update-btn");
    const dismissBtn = document.getElementById("sw-dismiss-btn");

    let refreshing = false;
    navigator.serviceWorker.addEventListener("controllerchange", () => {
      if (!refreshing) {
        refreshing = true;
        window.location.reload();
      }
    });

    window.addEventListener("load", () => {
      navigator.serviceWorker
        .register("/sw.js")
        .then((reg) => {
          reg.addEventListener("updatefound", () => {
            const newWorker = reg.installing;
            if (!newWorker) return;

            newWorker.addEventListener("statechange", () => {
              if (newWorker.state === "installed" && navigator.serviceWorker.controller) {
                if (updateToast) {
                  updateToast.classList.remove("hidden");
                  if (updateBtn) {
                    updateBtn.onclick = () => {
                      newWorker.postMessage({ type: "SKIP_WAITING" });
                    };
                  }
                  if (dismissBtn) {
                    dismissBtn.onclick = () => {
                      updateToast.classList.add("hidden");
                    };
                  }
                }
              }
            });
          });
        })
        .catch((err) => {
          console.warn("ServiceWorker registration failed:", err);
        });
    });
  }

  function renderBoard(board) {
    const st = board.station;
    const activeRoutes = board.active_routes || st.routes || [];
    const transferLinks = board.transfer_links || [];
    const activeLink = transferLinks.find((tl) => tl.is_active);
    const activeLineName = (activeLink && activeLink.line_name) || st.line;
    document.title = `MTA.rip — ${st.name} (${activeRoutes.join("/")}) — Unofficial NYC Subway Live Location Board`;

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
    const isOfflineSnapshot = board.data_source === "OFFLINE_SYSTEM_SNAPSHOT";
    if (isOfflineSnapshot) {
      feedSourceBadgeEl.textContent = "OFFLINE SNAPSHOT";
      feedSourceBadgeEl.className = "feed-badge stale";
    } else if (isSimulated) {
      feedSourceBadgeEl.textContent = "SIMULATED TRAIN POSITIONS";
      feedSourceBadgeEl.className = "feed-badge simulated";
    } else {
      feedSourceBadgeEl.textContent = "LIVE MTA GTFS-RT";
      feedSourceBadgeEl.className = "feed-badge";
    }

    const updatedTime = new Date().toLocaleTimeString("en-US", {
      hour12: false,
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    lastUpdatedTextEl.textContent = isOfflineSnapshot
      ? `Cached snapshot (Offline)`
      : `Updated ${updatedTime}`;

    renderDirectionPanel(
      board.northbound,
      northDirHeadingEl,
      northCountBadgeEl,
      northTrainsEl,
      st
    );
    renderDirectionPanel(
      board.southbound,
      southDirHeadingEl,
      southCountBadgeEl,
      southTrainsEl,
      st
    );
  }

  function resolveStationId(stopId, stopName, routeId) {
    if (stopId && allStations.some((s) => s.id === stopId)) {
      return stopId;
    }
    if (!stopName || stopName === "...") {
      return "";
    }
    if (routeId) {
      const byRoute = allStations.find(
        (s) => s.name === stopName && Array.isArray(s.routes) && s.routes.includes(routeId)
      );
      if (byRoute) return byRoute.id;
    }
    const byName = allStations.find((s) => s.name === stopName);
    return byName ? byName.id : "";
  }

  function createStationLink(stopName, stopId, routeId, className = "board-station-link") {
    const resolvedId = resolveStationId(stopId, stopName, routeId);
    if (!resolvedId) {
      const span = document.createElement("span");
      if (className && className !== "board-station-link") {
        span.className = className;
      }
      span.textContent = stopName;
      return span;
    }
    const a = document.createElement("a");
    const params = new URLSearchParams();
    params.set("station", resolvedId);
    if (routeId) {
      params.set("route", routeId);
    }
    if (currentView === "trackmap") {
      params.set("view", "trackmap");
    }
    a.href = `?${params.toString()}`;
    a.className = className;
    a.textContent = stopName;
    a.title = `Jump to ${stopName} station`;
    a.addEventListener("click", (e) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
        return;
      }
      e.preventDefault();
      selectStation(resolvedId, routeId);
    });
    return a;
  }

  function buildLocationStatusNode(tr, targetStation) {
    const statusText = document.createElement("span");
    const targetName = (targetStation && targetStation.name) || "";
    const targetId = (targetStation && targetStation.id) || "";
    const currName = tr.current_stop || targetName;
    const currId = tr.current_stop_id || targetId;

    if (tr.status_type === "BETWEEN" && tr.previous_stop && currName) {
      statusText.appendChild(document.createTextNode("Between "));
      statusText.appendChild(
        createStationLink(tr.previous_stop, tr.previous_stop_id, tr.route_id)
      );
      statusText.appendChild(document.createTextNode(" & "));
      statusText.appendChild(createStationLink(currName, currId, tr.route_id));
      return statusText;
    }

    if (tr.status_type === "AT_TARGET" && (targetName || currName)) {
      const stName = targetName || currName;
      const stId = targetId || currId;
      statusText.appendChild(document.createTextNode("At "));
      statusText.appendChild(createStationLink(stName, stId, tr.route_id));
      statusText.appendChild(document.createTextNode(" (Boarding in Station)"));
      return statusText;
    }

    if (tr.status_type === "APPROACHING" && currName) {
      statusText.appendChild(document.createTextNode("Approaching "));
      statusText.appendChild(createStationLink(currName, currId, tr.route_id));
      return statusText;
    }

    if (tr.status_type === "AT_STATION" && currName) {
      statusText.appendChild(document.createTextNode("At "));
      statusText.appendChild(createStationLink(currName, currId, tr.route_id));
      return statusText;
    }

    statusText.textContent = tr.location_status;
    return statusText;
  }

  function renderDirectionPanel(dirBoard, headingEl, countBadgeEl, containerEl, targetStation) {
    const targetStationName = (targetStation && targetStation.name) || "";
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
      destName.appendChild(
        createStationLink(tr.destination, tr.destination_stop_id, tr.route_id)
      );
      destName.title = tr.destination;

      const tripSub = document.createElement("div");
      tripSub.className = "train-trip-sub";
      tripSub.textContent = tr.track ? `Trip ${tr.trip_id} · Track ${tr.track}` : `Trip ${tr.trip_id}`;

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

      const statusText = buildLocationStatusNode(tr, targetStation);

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
          const isCurrent = idx === 0;
          const isTarget = idx === tr.recent_path.length - 1 && stopName === targetStationName;
          const nodeClass = `strip-node ${isCurrent ? "current" : ""} ${isTarget ? "target" : ""}`.trim();
          const stopId =
            Array.isArray(tr.recent_stop_ids) && tr.recent_stop_ids[idx]
              ? tr.recent_stop_ids[idx]
              : "";
          if (stopName === "...") {
            const node = document.createElement("span");
            node.className = nodeClass;
            node.textContent = stopName;
            strip.appendChild(node);
          } else {
            const node = createStationLink(stopName, stopId, tr.route_id, nodeClass);
            strip.appendChild(node);
          }
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
    let tmap = board && board.track_map;
    if (!tmap || !Array.isArray(tmap.nodes) || tmap.nodes.length === 0) {
      const targetSt =
        (board && board.station) ||
        stationMap[currentStationId] ||
        allStations.find((s) => s.id === currentStationId);
      if (targetSt) {
        const topo = getStationTopology(targetSt, currentFocusRoute);
        tmap = topo ? topo.trackMap : null;
        if (board) board.track_map = tmap;
      }
    }
    if (!tmap || !Array.isArray(tmap.nodes) || tmap.nodes.length === 0) {
      return;
    }

    const st =
      (board && board.station) ||
      stationMap[currentStationId] || { name: tmap.line_name, routes: [] };
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
            let carTitle = `${v.route_short_name} Train to ${v.destination}\n${v.status_label}`;
            if (v.track) {
              carTitle += `\nTrack: ${v.track}`;
            }
            car.title = carTitle;

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
