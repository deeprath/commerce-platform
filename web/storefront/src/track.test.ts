import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { track, flushEvents, toMinor } from "./track";

function mockFetch() {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 202 }));
}

function sentBodies(f: ReturnType<typeof mockFetch>): Array<{ events: Array<Record<string, unknown>> }> {
  return f.mock.calls.map((c) => JSON.parse((c[1] as RequestInit).body as string));
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  flushEvents(); // drain anything a test left queued
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("track", () => {
  it("batches events and flushes after the debounce window", () => {
    const f = mockFetch();
    track("page_view", { path: "/" });
    track("product_view", { product_id: "p1" });
    expect(f).not.toHaveBeenCalled(); // still buffered

    vi.advanceTimersByTime(2000);

    expect(f).toHaveBeenCalledTimes(1);
    const [body] = sentBodies(f);
    expect(body.events).toHaveLength(2);
    expect(body.events[0]).toMatchObject({ type: "page_view", path: "/" });
    expect(body.events[1]).toMatchObject({ type: "product_view", product_id: "p1" });
    expect(typeof body.events[0].client_time).toBe("string");
    expect("session_id" in body.events[0]).toBe(true);
  });

  it("flushes immediately once the queue hits the cap", () => {
    const f = mockFetch();
    for (let i = 0; i < 20; i++) track("search", { query: `q${i}` });
    expect(f).toHaveBeenCalledTimes(1);
    expect(sentBodies(f)[0].events).toHaveLength(20);
  });

  it("flushEvents is a no-op with an empty queue", () => {
    const f = mockFetch();
    flushEvents();
    expect(f).not.toHaveBeenCalled();
  });

  it("post-flush events start a fresh batch", () => {
    const f = mockFetch();
    track("add_to_cart", { product_id: "p1", value_minor: 100 });
    flushEvents();
    track("begin_checkout", { value_minor: 500 });
    flushEvents();
    expect(f).toHaveBeenCalledTimes(2);
    expect(sentBodies(f)[1].events).toHaveLength(1);
    expect(sentBodies(f)[1].events[0]).toMatchObject({ type: "begin_checkout" });
  });
});

describe("toMinor", () => {
  it("converts protojson Money to integer minor units", () => {
    expect(toMinor({ units: "75", nanos: 580000000 })).toBe(7558);
    expect(toMinor({ units: "12", nanos: 0 })).toBe(1200);
    expect(toMinor(undefined)).toBe(0);
    expect(toMinor({})).toBe(0);
  });
});
