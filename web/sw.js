// MTA.rip Service Worker — Subway Offline & Transit Wireless Resilience
const CACHE_SHELL = "mta-shell-v4";
const CACHE_CATALOG = "mta-catalog-v4";
const CACHE_TRAINS = "mta-trains-v4";
const CACHE_BOARDS = "mta-boards-v4";
const MAX_CACHED_BOARDS = 30;

const SHELL_ASSETS = [
  "/",
  "/index.html",
  "/styles.css",
  "/mta_catalog.js",
  "/app.js",
  "/manifest.webmanifest"
];

// Install: Precache App Shell, Stations catalog, and Trains snapshot
self.addEventListener("install", (event) => {
  self.skipWaiting();
  event.waitUntil(
    Promise.all([
      caches.open(CACHE_SHELL).then((cache) => cache.addAll(SHELL_ASSETS)),
      caches.open(CACHE_CATALOG).then(async (cache) => {
        try {
          const res = await fetch("/api/stations");
          if (isCleanJson(res)) await cache.put("/api/stations", res);
        } catch (_) {}
      }),
      caches.open(CACHE_TRAINS).then(async (cache) => {
        try {
          const res = await fetch("/api/trains");
          if (isCleanJson(res)) await cache.put("/api/trains", res);
        } catch (_) {}
      }),
    ])
  );
});

// Message listener: User accepted update via banner
self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") {
    self.skipWaiting();
  }
});

// Activate: Prune outdated caches
self.addEventListener("activate", (event) => {
  const allowed = [CACHE_SHELL, CACHE_CATALOG, CACHE_TRAINS, CACHE_BOARDS];
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys
          .filter((k) => !allowed.includes(k))
          .map((k) => caches.delete(k))
      )
    ).then(() => self.clients.claim())
  );
});

// Captive Portal & HTML Splash Protection
// Subway station Wi-Fi often returns HTTP 200 with HTML login page instead of JSON
function isCleanJson(response) {
  if (!response || !response.ok || response.redirected) return false;
  const ct = response.headers.get("content-type") || "";
  return ct.toLowerCase().includes("application/json");
}

// Canonicalize /api/board URLs so query param ordering differences don't miss cache
function canonicalizeBoardUrl(url) {
  const u = new URL(url);
  const st = u.searchParams.get("station") || "127";
  const rt = u.searchParams.get("route") || "";
  const canonical = new URL(u.origin + u.pathname);
  canonical.searchParams.set("station", st);
  if (rt) canonical.searchParams.set("route", rt);
  return canonical.toString();
}

async function networkFirstWithTimeout(request, cacheName, timeoutMs = 3000, isBoard = false) {
  const cacheKey = isBoard ? canonicalizeBoardUrl(request.url) : request;
  const cache = await caches.open(cacheName);
  const cachedRes = await cache.match(cacheKey);

  // If phone is in Airplane Mode, return cached response immediately without blocking for 3s
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    if (cachedRes) return cachedRes;
    return new Response(
      JSON.stringify({ offline: true, error: "Subway network unavailable" }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" }
      }
    );
  }

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const netRes = await fetch(request, { signal: controller.signal });
    clearTimeout(timer);
    if (isCleanJson(netRes)) {
      if (isBoard) {
        await enforceLruQuota(cache, MAX_CACHED_BOARDS);
      }
      await cache.put(cacheKey, netRes.clone());
      return netRes;
    }
    // If captive portal HTML was returned, fall back to clean cached JSON
    if (cachedRes) return cachedRes;
    return netRes;
  } catch (_) {
    clearTimeout(timer);
    // Timeout or network error in tunnel -> return cached response
    if (cachedRes) return cachedRes;
    return new Response(
      JSON.stringify({ offline: true, error: "Subway network unavailable" }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" }
      }
    );
  }
}

async function enforceLruQuota(cache, maxItems) {
  const keys = await cache.keys();
  if (keys.length >= maxItems) {
    // Delete oldest entry
    await cache.delete(keys[0]);
  }
}

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);

  // Only handle GET requests
  if (event.request.method !== "GET") {
    return;
  }

  // 1. Navigation Requests (e.g. reload or visiting with query params in airplane mode)
  // ALWAYS return the cached app shell immediately so the browser never shows "Offline" in the URL bar
  if (event.request.mode === "navigate") {
    event.respondWith(
      (async () => {
        const cache = await caches.open(CACHE_SHELL);
        const cached = (await cache.match("/")) || (await cache.match("/index.html"));
        if (cached) {
          if (typeof navigator !== "undefined" && navigator.onLine) {
            fetch(event.request)
              .then((res) => {
                if (res && res.ok) cache.put("/", res.clone());
              })
              .catch(() => {});
          }
          return cached;
        }
        try {
          return await fetch(event.request);
        } catch (_) {
          return (await cache.match("/")) || (await cache.match("/index.html"));
        }
      })()
    );
    return;
  }

  // 2. Station Catalog: Stale-While-Revalidate with Safe Offline Fallback
  if (url.pathname === "/api/stations") {
    event.respondWith(
      (async () => {
        const cache = await caches.open(CACHE_CATALOG);
        const cached = (await cache.match(event.request)) || (await cache.match("/api/stations"));
        if (typeof navigator !== "undefined" && navigator.onLine === false && cached) {
          return cached;
        }
        try {
          const res = await fetch(event.request);
          if (isCleanJson(res)) {
            await cache.put(event.request, res.clone());
            await cache.put("/api/stations", res.clone());
          }
          return res;
        } catch (_) {
          if (cached) return cached;
          return new Response(JSON.stringify({ offline: true, stations: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" }
          });
        }
      })()
    );
    return;
  }

  // 3. System-Wide Trains Snapshot: Network-First (3s Timeout)
  if (url.pathname === "/api/trains") {
    event.respondWith(networkFirstWithTimeout(event.request, CACHE_TRAINS, 3000, false));
    return;
  }

  // 4. Station Board: Network-First (3s Timeout) with LRU
  if (url.pathname === "/api/board") {
    event.respondWith(networkFirstWithTimeout(event.request, CACHE_BOARDS, 3000, true));
    return;
  }

  // 5. App Shell Static Assets (css, js, manifest): Cache-First
  if (SHELL_ASSETS.includes(url.pathname)) {
    event.respondWith(
      caches.open(CACHE_SHELL).then(async (cache) => {
        const cached = await cache.match(url.pathname);
        return cached || fetch(event.request);
      })
    );
    return;
  }

  // Default passthrough
  event.respondWith(fetch(event.request));
});
