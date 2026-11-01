// MTA.rip Service Worker — Subway Offline & Transit Wireless Resilience
const CACHE_SHELL = "mta-shell-v6";
const CACHE_API = "mta-api-v6";
const API_TIMEOUT_MS = 2500;

const SHELL_ASSETS = [
  "/",
  "/index.html",
  "/styles.css",
  "/mta_catalog.js",
  "/app.js",
  "/manifest.webmanifest",
];

// Helper: fetch with timeout and no-store cache to avoid HTTP stale layers
async function fetchWithTimeout(resource, timeoutMs = API_TIMEOUT_MS) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(resource, { signal: controller.signal, cache: "no-store" });
  } finally {
    clearTimeout(timer);
  }
}

// Captive Portal & HTML Splash Protection
// Subway station Wi-Fi often returns HTTP 200 with HTML login page instead of JSON or JS/CSS
async function isCleanJson(response, canonicalPath = "") {
  if (!response || !response.ok || response.redirected) return false;
  const ct = (response.headers.get("content-type") || "").toLowerCase();
  if (!ct.includes("application/json")) return false;
  if (canonicalPath) {
    try {
      const cloned = response.clone();
      const data = await cloned.json();
      if (canonicalPath === "/api/alerts" && !Array.isArray(data.alerts)) return false;
      if (canonicalPath === "/api/trains" && !Array.isArray(data.trains)) return false;
      if (canonicalPath === "/api/stations" && !Array.isArray(data.stations)) return false;
    } catch (_) {
      return false;
    }
  }
  return true;
}

function isCleanShellResponse(response, pathname) {
  if (!response || !response.ok || response.redirected) return false;
  const ct = (response.headers.get("content-type") || "").toLowerCase();
  if (pathname.endsWith(".js") && !ct.includes("javascript")) return false;
  if (pathname.endsWith(".css") && !ct.includes("css")) return false;
  if (pathname.endsWith(".webmanifest") && ct.includes("html")) return false;
  if ((pathname === "/" || pathname === "/index.html") && !ct.includes("html")) return false;
  return true;
}

// Install: Precache App Shell, and opportunistically pre-fetch API snapshot
self.addEventListener("install", (event) => {
  self.skipWaiting();
  event.waitUntil(
    (async () => {
      const shellCache = await caches.open(CACHE_SHELL);
      await shellCache.addAll(SHELL_ASSETS);

      const apiCache = await caches.open(CACHE_API);
      await Promise.allSettled([
        (async () => {
          try {
            const res = await fetchWithTimeout("/api/stations");
            if (await isCleanJson(res, "/api/stations")) await apiCache.put("/api/stations", res);
          } catch (_) {}
        })(),
        (async () => {
          try {
            const res = await fetchWithTimeout("/api/trains");
            if (await isCleanJson(res, "/api/trains")) await apiCache.put("/api/trains", res);
          } catch (_) {}
        })(),
        (async () => {
          try {
            const res = await fetchWithTimeout("/api/alerts");
            if (await isCleanJson(res, "/api/alerts")) await apiCache.put("/api/alerts", res);
          } catch (_) {}
        })(),
      ]);
    })()
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
  const allowed = [CACHE_SHELL, CACHE_API];
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((k) => !allowed.includes(k))
            .map((k) => caches.delete(k))
        )
      )
      .then(() => self.clients.claim())
  );
});

// Unified API Handler: Network-First with Lie-Fi Timeout and Cache Fallback
async function networkFirstApi(request, canonicalPath) {
  const cache = await caches.open(CACHE_API);
  const cachedRes = await cache.match(canonicalPath);

  if (typeof navigator !== "undefined" && navigator.onLine === false && cachedRes) {
    return cachedRes;
  }

  try {
    const netRes = await fetchWithTimeout(request, API_TIMEOUT_MS);
    if (await isCleanJson(netRes, canonicalPath)) {
      await cache.put(canonicalPath, netRes.clone());
      return netRes;
    }
  } catch (_) {
    // Timeout or network error in tunnel -> fall through to cached/offline response
  }

  if (cachedRes) return cachedRes;
  const fallback =
    canonicalPath === "/api/alerts"
      ? { offline: true, alerts: [], feed_timestamp: 0 }
      : canonicalPath === "/api/trains"
      ? { offline: true, trains: [], feed_timestamp: 0 }
      : { offline: true, error: "Subway network unavailable" };

  return new Response(JSON.stringify(fallback), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

// Unified App Shell Handler: Cache-First with Background Revalidation
async function cacheFirstShell(request, cacheKey) {
  const cache = await caches.open(CACHE_SHELL);
  let cached = await cache.match(cacheKey);
  if (!cached && (cacheKey === "/" || cacheKey === "/index.html")) {
    cached = (await cache.match("/")) || (await cache.match("/index.html"));
  }

  if (cached) {
    if (typeof navigator !== "undefined" && navigator.onLine) {
      fetchWithTimeout(request, API_TIMEOUT_MS)
        .then((res) => {
          if (isCleanShellResponse(res, cacheKey)) {
            cache.put(cacheKey, res.clone());
          }
        })
        .catch(() => {});
    }
    return cached;
  }

  try {
    const netRes = await fetchWithTimeout(request, API_TIMEOUT_MS);
    if (isCleanShellResponse(netRes, cacheKey)) {
      await cache.put(cacheKey, netRes.clone());
      return netRes;
    }
  } catch (_) {}

  return (
    cached ||
    (await cache.match("/")) ||
    (await cache.match("/index.html")) ||
    fetch(request)
  );
}

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);

  if (event.request.method !== "GET") {
    return;
  }

  if (event.request.mode === "navigate") {
    event.respondWith(cacheFirstShell(event.request, "/"));
    return;
  }

  if (url.pathname === "/api/stations") {
    event.respondWith(networkFirstApi(event.request, "/api/stations"));
    return;
  }

  if (url.pathname === "/api/trains") {
    event.respondWith(networkFirstApi(event.request, "/api/trains"));
    return;
  }

  if (url.pathname === "/api/alerts") {
    event.respondWith(networkFirstApi(event.request, "/api/alerts"));
    return;
  }

  if (SHELL_ASSETS.includes(url.pathname)) {
    event.respondWith(cacheFirstShell(event.request, url.pathname));
    return;
  }

  event.respondWith(fetch(event.request));
});
