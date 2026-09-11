import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import { Login } from "./Login";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Login (admin)", () => {
  it("disables Sign in until a password is entered", () => {
    render(<Login onAuthed={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
  });

  it("signs in and calls onAuthed", async () => {
    const user = userEvent.setup();
    const login = vi.spyOn(api, "login").mockResolvedValue({ authenticated: true });
    const onAuthed = vi.fn();
    render(<Login onAuthed={onAuthed} />);

    await user.clear(screen.getByLabelText("Username"));
    await user.type(screen.getByLabelText("Username"), "adminuser");
    await user.type(screen.getByLabelText("Password"), "adminuser123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(login).toHaveBeenCalledWith("adminuser", "adminuser123");
    expect(onAuthed).toHaveBeenCalled();
  });

  it("shows a friendly message for bad credentials", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "login").mockRejectedValue(
      new ApiRequestError({ status: 401, code: "UNAUTHENTICATED", reason: "BAD_CREDENTIALS" }),
    );
    render(<Login onAuthed={vi.fn()} />);

    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Invalid username or password.")).toBeInTheDocument();
  });

  it("shows a generic message for any other failure", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "login").mockRejectedValue(new Error("network down"));
    render(<Login onAuthed={vi.fn()} />);

    await user.type(screen.getByLabelText("Password"), "x");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Sign-in failed. Try again.")).toBeInTheDocument();
  });
});
