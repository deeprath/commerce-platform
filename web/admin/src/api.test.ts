import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError, setUnauthorizedHandler, _qs } from "./api";
import { label, money, when } from "./types";

afterEach(() => {
  vi.restoreAllMocks();
  setUnauthorizedHandler(null);
});

function mockFetch(status: number, body: unknown) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
    new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

describe("_qs", () => {
  it("drops empty and undefined values", () => {
    expect(_qs({ status: "REQUESTED", owner_id: "", page_size: 20, extra: undefined })).toBe(
      "?status=REQUESTED&page_size=20",
    );
  });
  it("is empty when nothing is set", () => {
    expect(_qs({ a: "", b: undefined })).toBe("");
  });
});

describe("api", () => {
  it("forwards admin list filters onto the query string", async () => {
    const f = mockFetch(200, { orders: [] });
    await api.listOrders({ status: "FULFILLED", owner_id: "u1" });
    expect(f.mock.calls[0][0]).toBe("/api/v1/admin/orders?status=FULFILLED&owner_id=u1");
  });

  it("posts the decision body", async () => {
    const f = mockFetch(200, { id: "r1", status: "RETURN_STATUS_APPROVED" });
    await api.decideReturn("r1", true, "ok");
    const init = f.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ approve: true, note: "ok" });
  });

  it("fires the unauthorized handler on 401", async () => {
    mockFetch(401, { code: "UNAUTHENTICATED", reason: "SIGN_IN_REQUIRED" });
    const h = vi.fn();
    setUnauthorizedHandler(h);
    await expect(api.listOrders()).rejects.toBeInstanceOf(ApiRequestError);
    expect(h).toHaveBeenCalledOnce();
  });
});

describe("formatters", () => {
  it("money combines units and nanos", () => {
    expect(money({ currency_code: "USD", units: "37", nanos: 790000000 })).toBe("$37.79");
    expect(money(null)).toBe("—");
  });
  it("label strips the proto enum prefix", () => {
    expect(label("ORDER_STATUS_PENDING_PAYMENT")).toBe("Pending payment");
    expect(label("RETURN_STATUS_REQUESTED")).toBe("Requested");
  });
  it("when formats or passes through", () => {
    expect(when()).toBe("—");
    expect(when("not-a-date")).toBe("not-a-date");
  });
});
