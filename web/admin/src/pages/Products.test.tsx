import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import { Products } from "./Products";

afterEach(() => {
  vi.restoreAllMocks();
});

const HIT = {
  product_id: "p1",
  slug: "trail-cap",
  title: "Trail Cap",
  list_price: { currency_code: "USD", units: "24", nanos: 500000000 },
};

function listResponse(hits: (typeof HIT)[] = [HIT]) {
  return { hits };
}

async function fillCreateForm(user: ReturnType<typeof userEvent.setup>, price: string) {
  await user.type(screen.getByLabelText("Slug"), "new-hat");
  await user.type(screen.getByLabelText("Title"), "New Hat");
  const priceInput = screen.getByLabelText(/price/i);
  await user.clear(priceInput);
  if (price) await user.type(priceInput, price);
}

describe("Products", () => {
  it("shows a loading state, then the product list", async () => {
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    render(<Products />);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByText("Trail Cap")).toBeInTheDocument();
    expect(screen.getByText("trail-cap")).toBeInTheDocument();
    expect(screen.getByText("$24.50")).toBeInTheDocument();
  });

  it("shows an empty state with no products", async () => {
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse([]));
    render(<Products />);
    expect(await screen.findByText("No active products.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listProducts").mockRejectedValue(new Error("catalog is down"));
    render(<Products />);
    expect(await screen.findByText("catalog is down")).toBeInTheDocument();
  });

  it("maps a 403 to a friendlier message than the raw reason", async () => {
    vi.spyOn(api, "listProducts").mockRejectedValue(
      new ApiRequestError({ status: 403, code: "FORBIDDEN", reason: "NOT_AN_OPERATOR" }),
    );
    render(<Products />);
    expect(await screen.findByText(/doesn.t have an operator role/)).toBeInTheDocument();
  });

  it("refetches when Refresh is clicked", async () => {
    const user = userEvent.setup();
    const list = vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    render(<Products />);
    await screen.findByText("Trail Cap");
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it("creates a product, splitting whole dollars from cents into units/nanos", async () => {
    const user = userEvent.setup();
    const list = vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    const create = vi.spyOn(api, "createProduct").mockResolvedValue({
      id: "p2", slug: "new-hat", title: "New Hat", description: "", category_id: "general",
      list_price: { currency_code: "USD", units: "19", nanos: 990000000 }, media_keys: [], status: "ACTIVE", attributes: {},
    });
    render(<Products />);
    await screen.findByText("Trail Cap");

    await fillCreateForm(user, "19.99");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({
          slug: "new-hat",
          title: "New Hat",
          category_id: "general",
          list_price: { currency_code: "USD", units: "19", nanos: 990000000 },
        }),
      ),
    );
    expect(await screen.findByText("Created “New Hat”.")).toBeInTheDocument();
    // The form resets after a successful create.
    expect(screen.getByLabelText("Slug")).toHaveValue("");
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it("rejects a non-numeric price without calling the API", async () => {
    // Regression test: an earlier version of this check (`!(dollars > 0)`)
    // would accept Number("abc") === NaN, since NaN <= 0 is false. The
    // Number.isFinite guard must actually reject it.
    const user = userEvent.setup();
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    const create = vi.spyOn(api, "createProduct");
    render(<Products />);
    await screen.findByText("Trail Cap");

    await fillCreateForm(user, "abc");
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("Enter a price in dollars.")).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
  });

  it("rejects a zero or negative price without calling the API", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    const create = vi.spyOn(api, "createProduct");
    render(<Products />);
    await screen.findByText("Trail Cap");

    await fillCreateForm(user, "-5");
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("Enter a price in dollars.")).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
  });

  it("shows the API's reason when creation fails server-side", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    vi.spyOn(api, "createProduct").mockRejectedValue(
      new ApiRequestError({ status: 409, code: "CONFLICT", reason: "SLUG_TAKEN" }),
    );
    render(<Products />);
    await screen.findByText("Trail Cap");

    await fillCreateForm(user, "10");
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("SLUG_TAKEN")).toBeInTheDocument();
  });

  it("archives a product and reloads the list", async () => {
    const user = userEvent.setup();
    const list = vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    const archive = vi.spyOn(api, "archiveProduct").mockResolvedValue({
      id: "p1", slug: "trail-cap", title: "Trail Cap", description: "", category_id: "general",
      list_price: HIT.list_price, media_keys: [], status: "ARCHIVED", attributes: {},
    });
    render(<Products />);
    await screen.findByText("Trail Cap");

    await user.click(screen.getByRole("button", { name: "Archive" }));

    expect(archive).toHaveBeenCalledWith("p1");
    expect(await screen.findByText("Archived.")).toBeInTheDocument();
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it("shows the API's reason when archiving fails", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listProducts").mockResolvedValue(listResponse());
    vi.spyOn(api, "archiveProduct").mockRejectedValue(
      new ApiRequestError({ status: 404, code: "NOT_FOUND", reason: "PRODUCT_NOT_FOUND" }),
    );
    render(<Products />);
    await screen.findByText("Trail Cap");

    await user.click(screen.getByRole("button", { name: "Archive" }));

    expect(await screen.findByText("PRODUCT_NOT_FOUND")).toBeInTheDocument();
  });
});
