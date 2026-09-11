import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import { Login } from "./Login";

const navigate = vi.fn();
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useNavigate: () => navigate };
});

afterEach(() => {
  vi.restoreAllMocks();
  navigate.mockClear();
});

function renderPage(onAuthed = vi.fn()) {
  return { onAuthed, ...render(
    <MemoryRouter>
      <Login onAuthed={onAuthed} />
    </MemoryRouter>,
  ) };
}

describe("Login", () => {
  it("disables Sign in until a password is entered", () => {
    renderPage();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
  });

  it("signs in, calls onAuthed, and navigates home", async () => {
    const user = userEvent.setup();
    const login = vi.spyOn(api, "login").mockResolvedValue({ authenticated: true, expires_in: 3600 });
    const { onAuthed } = renderPage();

    await user.clear(screen.getByLabelText("Username"));
    await user.type(screen.getByLabelText("Username"), "testuser");
    await user.type(screen.getByLabelText("Password"), "testuser123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => expect(login).toHaveBeenCalledWith("testuser", "testuser123"));
    expect(onAuthed).toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith("/");
  });

  it("shows a friendly message for bad credentials", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "login").mockRejectedValue(
      new ApiRequestError({ status: 401, code: "UNAUTHENTICATED", reason: "BAD_CREDENTIALS" }),
    );
    renderPage();

    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Invalid username or password.")).toBeInTheDocument();
  });

  it("shows a generic message for any other failure", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "login").mockRejectedValue(new Error("network down"));
    renderPage();

    await user.type(screen.getByLabelText("Password"), "x");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Sign-in failed. Try again.")).toBeInTheDocument();
  });
});
