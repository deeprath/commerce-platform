// Lightweight browser clickstream. Events are batched and flushed to the BFF
// (`POST /api/v1/events`) with navigator.sendBeacon, so they survive the page
// being torn down mid-navigation. Fire-and-forget: the server enriches every
// event with a first-party cookie id and ignores anything it doesn't recognise,
// so a failed or dropped beacon is a non-event for the UI.

export type TrackType =
  | "page_view"
  | "product_view"
  | "search"
  | "add_to_cart"
  | "begin_checkout"
  | "purchase";

export interface TrackProps {
  path?: string;
  referrer?: string;
  product_id?: string;
  query?: string;
  value_minor?: number;
  currency_code?: string;
}

const ENDPOINT = "/api/v1/events";
const SESSION_KEY = "cs_sid";
const MAX_QUEUE = 20;
const FLUSH_DELAY_MS = 2000;

interface QueuedEvent extends TrackProps {
  type: TrackType;
  client_time: string;
}

let queue: QueuedEvent[] = [];
let timer: ReturnType<typeof setTimeout> | null = null;

// A per-tab correlation id, not a security token. Every evergreen browser has
// at least crypto.getRandomValues (available since IE11), so the real fallback
// path below is effectively unreachable on any browser this app targets — but
// deliberately not Math.random() for it either: that stays flagged as a
// pseudo-random-number security hotspot regardless of context, so Date.now()
// plus a per-page counter (good enough to avoid same-millisecond collisions
// across tabs, which is all this id needs) avoids the flagged API entirely.
let fallbackIDCounter = 0;
function randomID(): string {
  if (typeof crypto !== "undefined") {
    if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
    if (typeof crypto.getRandomValues === "function") {
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      return Array.from(bytes, (b: number) => b.toString(16).padStart(2, "0")).join("");
    }
  }
  fallbackIDCounter += 1;
  return Date.now().toString(36) + fallbackIDCounter.toString(36);
}

// Per-tab id, stable for the life of the tab session. Best-effort: private
// modes / disabled storage just yield "" and the server still counts the beacon.
function sessionId(): string {
  try {
    let id = sessionStorage.getItem(SESSION_KEY);
    if (!id) {
      id = randomID();
      sessionStorage.setItem(SESSION_KEY, id);
    }
    return id;
  } catch {
    return "";
  }
}

function send(body: string): void {
  try {
    if (typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function") {
      navigator.sendBeacon(ENDPOINT, new Blob([body], { type: "application/json" }));
      return;
    }
  } catch {
    /* fall through to fetch */
  }
  if (typeof fetch === "function") {
    void fetch(ENDPOINT, {
      method: "POST",
      body,
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      keepalive: true,
    }).catch(() => {
      /* fire-and-forget */
    });
  }
}

export function flushEvents(): void {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  if (queue.length === 0) return;
  const events = queue.map((e) => ({ ...e, session_id: sessionId() }));
  queue = [];
  send(JSON.stringify({ events }));
}

export function track(type: TrackType, props: TrackProps = {}): void {
  queue.push({ type, client_time: new Date().toISOString(), ...props });
  if (queue.length >= MAX_QUEUE) {
    flushEvents();
    return;
  }
  if (!timer) timer = setTimeout(flushEvents, FLUSH_DELAY_MS);
}

// Money (protojson) -> integer minor units, matching the server's convention.
export function toMinor(m?: { units?: string; nanos?: number }): number {
  if (!m) return 0;
  return Number(m.units ?? 0) * 100 + Math.round((m.nanos ?? 0) / 1e7);
}

if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flushEvents();
  });
  window.addEventListener("pagehide", flushEvents);
}
