import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";

function renderApp(path = "/orders") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("App (admin)", () => {
  it("shows the Login page when not authed", () => {
    renderApp();
    expect(screen.getByRole("heading", { name: "◆ Commerce Admin" })).toBeInTheDocument();
    expect(screen.queryByText("Sign out")).not.toBeInTheDocument();
  });

  it("shows the shell (nav + Sign out) once authed", async () => {
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderApp();
    expect(await screen.findByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Products" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Orders" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Returns" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Shipments" })).toBeInTheDocument();
  });

  it("logging in flips to the shell and navigates to /orders", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "login").mockResolvedValue({ authenticated: true });
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderApp("/");

    await user.type(screen.getByLabelText("Password"), "adminuser123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(localStorage.getItem("admin_signed_in")).toBe("1");
    // Landed on Orders regardless of the entry path.
    await waitFor(() => expect(screen.getByText("No orders.")).toBeInTheDocument());
  });

  it("signs out: clears the flag, calls the API, and reverts to Login", async () => {
    const user = userEvent.setup();
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    const logout = vi.spyOn(api, "logout").mockResolvedValue(undefined);
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(logout).toHaveBeenCalled());
    expect(localStorage.getItem("admin_signed_in")).toBeNull();
    expect(await screen.findByRole("heading", { name: "◆ Commerce Admin" })).toBeInTheDocument();
  });

  it("signs out locally even if the logout call fails", async () => {
    const user = userEvent.setup();
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    vi.spyOn(api, "logout").mockRejectedValue(new Error("down"));
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(localStorage.getItem("admin_signed_in")).toBeNull();
    expect(await screen.findByRole("heading", { name: "◆ Commerce Admin" })).toBeInTheDocument();
  });

  it("routes /products to the Products page", async () => {
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(api, "listProducts").mockResolvedValue({ hits: [] });
    renderApp("/products");
    expect(await screen.findByRole("heading", { name: "Products" })).toBeInTheDocument();
  });

  it("falls back to Orders for an unknown route", async () => {
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderApp("/nope");
    expect(await screen.findByRole("heading", { name: "Orders" })).toBeInTheDocument();
  });

  it("a 401 on any request drops the admin_signed_in flag and reverts to Login", async () => {
    localStorage.setItem("admin_signed_in", "1");
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ code: "UNAUTHENTICATED", reason: "TOKEN_EXPIRED" }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    );
    renderApp();

    expect(await screen.findByRole("heading", { name: "◆ Commerce Admin" })).toBeInTheDocument();
    expect(localStorage.getItem("admin_signed_in")).toBeNull();
  });
});
