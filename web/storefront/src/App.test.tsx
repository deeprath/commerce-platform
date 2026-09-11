import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";
import * as trackModule from "./track";

vi.mock("./track", () => ({ track: vi.fn(), toMinor: () => 0 }));

function renderApp(path = "/") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

function emptyCart() {
  return { id: "c_1", items: [], total_quantity: 0 };
}
function emptySearch() {
  return { hits: [], facets: [], page: { next_page_token: "", total_size: "0" } };
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("App", () => {
  it("shows Sign in when not authed, and never Orders/Seller/Sign out", async () => {
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    renderApp();

    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Orders" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Seller" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
  });

  it("shows Orders/Seller/Sign out when the signed_in flag is set", async () => {
    localStorage.setItem("signed_in", "1");
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    renderApp();

    expect(await screen.findByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Orders" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Seller" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Sign in" })).not.toBeInTheDocument();
  });

  it("signs out: clears the flag, calls the API, and reverts the header", async () => {
    const user = userEvent.setup();
    localStorage.setItem("signed_in", "1");
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    const logout = vi.spyOn(api, "logout").mockResolvedValue(undefined);
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(logout).toHaveBeenCalled());
    expect(localStorage.getItem("signed_in")).toBeNull();
    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
  });

  it("signs out locally even if the logout call fails", async () => {
    const user = userEvent.setup();
    localStorage.setItem("signed_in", "1");
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    vi.spyOn(api, "logout").mockRejectedValue(new Error("down"));
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(localStorage.getItem("signed_in")).toBeNull();
    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
  });

  it("shows the cart badge once the cart has items", async () => {
    vi.spyOn(api, "getCart").mockResolvedValue({ ...emptyCart(), total_quantity: 3 });
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    renderApp();
    expect(await screen.findByText("3")).toBeInTheDocument();
  });

  it("tracks a page_view for the current path", async () => {
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    vi.spyOn(api, "browse").mockResolvedValue(emptySearch());
    renderApp("/login");
    await waitFor(() =>
      expect(trackModule.track).toHaveBeenCalledWith("page_view", expect.objectContaining({ path: "/login" })),
    );
  });

  it("renders a 404 for an unknown route", async () => {
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    renderApp("/this-page-does-not-exist");
    expect(await screen.findByText("Not found.")).toBeInTheDocument();
  });

  it("routes /login to the Login page", async () => {
    vi.spyOn(api, "getCart").mockResolvedValue(emptyCart());
    renderApp("/login");
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  it("a 401 on any request drops the signed_in flag and reverts the header", async () => {
    localStorage.setItem("signed_in", "1");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes("/cart")) {
        return new Response(JSON.stringify(emptyCart()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      // Every other call (Browse's /catalog/products) comes back unauthenticated.
      return new Response(JSON.stringify({ code: "UNAUTHENTICATED", reason: "TOKEN_EXPIRED" }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      });
    });
    renderApp();

    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
    expect(localStorage.getItem("signed_in")).toBeNull();
  });
});
