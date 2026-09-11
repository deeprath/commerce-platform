import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import * as trackModule from "../track";
import type { Hit, SearchResponse } from "../types";
import { Browse } from "./Browse";

vi.mock("../track", () => ({ track: vi.fn() }));

afterEach(() => {
  vi.restoreAllMocks();
  vi.mocked(trackModule.track).mockClear();
});

const HIT: Hit = {
  product_id: "p1",
  slug: "trail-cap",
  title: "Trail Cap",
  category_id: "apparel",
  list_price: { currency_code: "USD", units: "24", nanos: 0 },
  primary_media_key: "",
  score: 1,
};

function response(overrides: Partial<SearchResponse> = {}): SearchResponse {
  return {
    hits: [HIT],
    facets: [
      {
        field: "category_id",
        values: [
          { value: "apparel", count: "3" },
          { value: "camping", count: "1" },
        ],
      },
    ],
    page: { next_page_token: "", total_size: "1" },
    ...overrides,
  };
}

function renderPage(initialPath = "/") {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Browse />
    </MemoryRouter>,
  );
}

describe("Browse", () => {
  it("loads with empty query params on first render", async () => {
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    await waitFor(() =>
      expect(browse).toHaveBeenCalledWith({ q: "", category_id: "", sort: undefined, page_size: 24 }),
    );
  });

  it("shows results with a count and product links", async () => {
    vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    expect(await screen.findByText("1 results")).toBeInTheDocument();
    const link = screen.getByRole("link", { name: /Trail Cap/ });
    expect(link).toHaveAttribute("href", "/p/trail-cap");
  });

  it("shows an empty-results message", async () => {
    vi.spyOn(api, "browse").mockResolvedValue(response({ hits: [], page: { next_page_token: "", total_size: "0" } }));
    renderPage();
    expect(await screen.findByText("No products match your search.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "browse").mockRejectedValue(new Error("search is down"));
    renderPage();
    expect(await screen.findByText(/search is down/)).toBeInTheDocument();
  });

  it("pre-fills the search box from the ?q= param and tracks the search", async () => {
    vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage("/?q=trail");
    expect(screen.getByLabelText("Search products")).toHaveValue("trail");
    await waitFor(() =>
      expect(api.browse).toHaveBeenCalledWith({ q: "trail", category_id: "", sort: undefined, page_size: 24 }),
    );
    expect(trackModule.track).toHaveBeenCalledWith("search", { query: "trail" });
  });

  it("does not track a search for the default empty query", async () => {
    vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    await screen.findByText("1 results");
    expect(trackModule.track).not.toHaveBeenCalled();
  });

  it("submitting the search form updates the query and refetches", async () => {
    const user = userEvent.setup();
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    await screen.findByText("1 results");

    await user.type(screen.getByLabelText("Search products"), "cap");
    await user.click(screen.getByRole("button", { name: "Search" }));

    await waitFor(() =>
      expect(browse).toHaveBeenLastCalledWith({ q: "cap", category_id: "", sort: undefined, page_size: 24 }),
    );
  });

  it("clears the query param when submitting an empty search box", async () => {
    const user = userEvent.setup();
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage("/?q=trail");
    await screen.findByText("1 results");

    await user.clear(screen.getByLabelText("Search products"));
    await user.click(screen.getByRole("button", { name: "Search" }));

    await waitFor(() =>
      expect(browse).toHaveBeenLastCalledWith({ q: "", category_id: "", sort: undefined, page_size: 24 }),
    );
  });

  it("refetches with the chosen sort", async () => {
    const user = userEvent.setup();
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    await screen.findByText("1 results");

    await user.selectOptions(screen.getByRole("combobox"), "price_asc");

    await waitFor(() =>
      expect(browse).toHaveBeenLastCalledWith({ q: "", category_id: "", sort: "price_asc", page_size: 24 }),
    );
  });

  it("renders category facet buttons and filters on click", async () => {
    const user = userEvent.setup();
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage();
    await screen.findByText("1 results");

    expect(screen.getByRole("button", { name: /apparel/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /camping/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "All" })).toHaveClass("active");

    await user.click(screen.getByRole("button", { name: /apparel/ }));

    await waitFor(() =>
      expect(browse).toHaveBeenLastCalledWith({ q: "", category_id: "apparel", sort: undefined, page_size: 24 }),
    );
    expect(screen.getByRole("button", { name: /apparel/ })).toHaveClass("active");
  });

  it("clears the category filter via the All button", async () => {
    const user = userEvent.setup();
    const browse = vi.spyOn(api, "browse").mockResolvedValue(response());
    renderPage("/?category_id=apparel");
    await screen.findByText("1 results");

    await user.click(screen.getByRole("button", { name: "All" }));

    await waitFor(() =>
      expect(browse).toHaveBeenLastCalledWith({ q: "", category_id: "", sort: undefined, page_size: 24 }),
    );
  });

  it("hides the category facet section when there is none", async () => {
    vi.spyOn(api, "browse").mockResolvedValue(response({ facets: [] }));
    renderPage();
    await screen.findByText("1 results");
    expect(screen.queryByText("Category")).not.toBeInTheDocument();
  });
});
