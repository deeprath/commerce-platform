import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { SellerStaff } from "./SellerStaff";

afterEach(() => {
  vi.restoreAllMocks();
});

function renderPage(authed: boolean) {
  return render(
    <MemoryRouter>
      <SellerStaff authed={authed} />
    </MemoryRouter>,
  );
}

describe("SellerStaff", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const list = vi.spyOn(api, "listShopStaff");
    renderPage(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listShopStaff").mockRejectedValue(new Error("network down"));
    renderPage(true);
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("lists the owner and staff subjects", async () => {
    vi.spyOn(api, "listShopStaff").mockResolvedValue({
      owner_subject: "owner-1",
      staff_subjects: ["staff-a", "staff-b"],
    });
    renderPage(true);
    await screen.findByText("owner-1");
    expect(screen.getByText("(owner)")).toBeInTheDocument();
    expect(screen.getByText("staff-a")).toBeInTheDocument();
    expect(screen.getByText("staff-b")).toBeInTheDocument();
    // Only staff entries get a Remove button, not the owner.
    expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(2);
  });

  it("adds a staff member and reloads the list", async () => {
    const user = userEvent.setup();
    const list = vi
      .spyOn(api, "listShopStaff")
      .mockResolvedValueOnce({ owner_subject: "owner-1", staff_subjects: [] })
      .mockResolvedValueOnce({ owner_subject: "owner-1", staff_subjects: ["staff-a"] });
    const add = vi.spyOn(api, "addShopStaff").mockResolvedValue(undefined);
    renderPage(true);

    await screen.findByText("owner-1");
    await user.type(screen.getByLabelText(/add staff/i), "staff-a");
    await user.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => expect(add).toHaveBeenCalledWith("staff-a"));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("staff-a")).toBeInTheDocument();
    // The input clears after a successful add.
    expect(screen.getByLabelText(/add staff/i)).toHaveValue("");
  });

  it("disables Add for a blank/whitespace-only subject", async () => {
    vi.spyOn(api, "listShopStaff").mockResolvedValue({ owner_subject: "owner-1", staff_subjects: [] });
    renderPage(true);
    await screen.findByText("owner-1");
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("removes a staff member after confirmation and reloads", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const list = vi
      .spyOn(api, "listShopStaff")
      .mockResolvedValueOnce({ owner_subject: "owner-1", staff_subjects: ["staff-a"] })
      .mockResolvedValueOnce({ owner_subject: "owner-1", staff_subjects: [] });
    const remove = vi.spyOn(api, "removeShopStaff").mockResolvedValue(undefined);
    renderPage(true);

    await screen.findByText("staff-a");
    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(remove).toHaveBeenCalledWith("staff-a");
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it("does not remove when the confirmation is declined", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(false);
    vi.spyOn(api, "listShopStaff").mockResolvedValue({ owner_subject: "owner-1", staff_subjects: ["staff-a"] });
    const remove = vi.spyOn(api, "removeShopStaff");
    renderPage(true);

    await screen.findByText("staff-a");
    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(remove).not.toHaveBeenCalled();
  });
});
