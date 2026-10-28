// MTA.rip Service Worker — Subway Offline & Transit Wireless Resilience
const CACHE_SHELL = "mta-shell-v1";
const CACHE_CATALOG = "mta-catalog-v1";
const CACHE_TRAINS = "mta-trains-v1";
const CACHE_BOARDS = "mta-boards-v1";
const MAX_CACHED_BOARDS = 30;

const SHELL_ASSETS = [
  "/",
  "/index.html",
  "/styles.css",
  "/app.js",
  "/manifest.webmanifest"
];

// Install: Precache App Shell
self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE_SHELL).then((cache) => cache.addAll(SHELL_ASSETS))
  );
  // Do NOT auto skipWaiting; wait for user tap on update banner
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

  // 1. Station Catalog: Stale-While-Revalidate
  if (url.pathname === "/api/stations") {
    event.respondWith(
      caches.open(CACHE_CATALOG).then(async (cache) => {
        const cached = await cache.match(event.request);
        const fetchPromise = fetch(event.request)
          .then((res) => {
            if (isCleanJson(res)) {
              cache.put(event.request, res.clone());
            }
            return res;
          })
          .catch(() => cached);
        return cached || fetchPromise;
      })
    );
    return;
  }

  // 2. System-Wide Trains Snapshot: Network-First (3s Timeout)
  if (url.pathname === "/api/trains") {
    event.respondWith(networkFirstWithTimeout(event.request, CACHE_TRAINS, 3000, false));
    return;
  }

  // 3. Station Board: Network-First (3s Timeout) with LRU
  if (url.pathname === "/api/board") {
    event.respondWith(networkFirstWithTimeout(event.request, CACHE_BOARDS, 3000, true));
    return;
  }

  // 4. App Shell Assets: Cache-First
  if (SHELL_ASSETS.includes(url.pathname) || url.pathname === "/") {
    event.respondWith(
      caches.match(event.request).then((cached) => cached || fetch(event.request))
    );
    return;
  }

  // Default passthrough
  event.respondWith(fetch(event.request));
});
