import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import type { Shipment } from "../types";
import { Shipments } from "./Shipments";

afterEach(() => {
  vi.restoreAllMocks();
});

const PENDING: Shipment = {
  id: "ship_1234567890",
  order_id: "order_1234567890",
  owner_id: "owner_1",
  status: "SHIPMENT_STATUS_PENDING",
  carrier: "",
  tracking_number: "",
  items: [],
  created_at: "2026-01-01T00:00:00Z",
  shipped_at: "",
  delivered_at: "",
  cancel_reason: "",
};

describe("Shipments", () => {
  it("shows a loading state, then an empty state", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [] });
    render(<Shipments />);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByText("No shipments.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listShipments").mockRejectedValue(new Error("network down"));
    render(<Shipments />);
    expect(await screen.findByText("network down")).toBeInTheDocument();
  });

  it("shows an em dash when there's no tracking number", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [PENDING] });
    render(<Shipments />);
    expect(await screen.findByText("—")).toBeInTheDocument();
  });

  it("shows carrier and tracking when present", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({
      shipments: [{ ...PENDING, carrier: "UPS", tracking_number: "TRK-123" }],
    });
    render(<Shipments />);
    expect(await screen.findByText("UPS · TRK-123")).toBeInTheDocument();
  });

  it("a pending shipment offers Ship and Cancel, not Mark delivered", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [PENDING] });
    render(<Shipments />);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: "Ship" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark delivered" })).not.toBeInTheDocument();
  });

  it("ships a pending shipment", async () => {
    const user = userEvent.setup();
    const listShipments = vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [PENDING] });
    const shipShipment = vi.spyOn(api, "shipShipment").mockResolvedValue({ ...PENDING, status: "SHIPMENT_STATUS_SHIPPED" });
    render(<Shipments />);
    await screen.findByRole("table");

    await user.click(screen.getByRole("button", { name: "Ship" }));

    expect(shipShipment).toHaveBeenCalledWith("ship_1234567890", "MANUAL", "TRK-ship_123");
    expect(await screen.findByText("Shipment #ship_123 → Shipped")).toBeInTheDocument();
    await waitFor(() => expect(listShipments).toHaveBeenCalledTimes(2));
  });

  it("a shipped shipment offers Mark delivered and Cancel, not Ship", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({
      shipments: [{ ...PENDING, status: "SHIPMENT_STATUS_SHIPPED" }],
    });
    render(<Shipments />);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: "Mark delivered" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ship" })).not.toBeInTheDocument();
  });

  it("marks a shipped shipment delivered", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [{ ...PENDING, status: "SHIPMENT_STATUS_SHIPPED" }] });
    const deliverShipment = vi.spyOn(api, "deliverShipment").mockResolvedValue({ ...PENDING, status: "SHIPMENT_STATUS_DELIVERED" });
    render(<Shipments />);
    await screen.findByRole("table");

    await user.click(screen.getByRole("button", { name: "Mark delivered" }));

    expect(deliverShipment).toHaveBeenCalledWith("ship_1234567890");
    expect(await screen.findByText("Shipment #ship_123 → Delivered")).toBeInTheDocument();
  });

  it("a delivered shipment offers no actions at all", async () => {
    vi.spyOn(api, "listShipments").mockResolvedValue({
      shipments: [{ ...PENDING, status: "SHIPMENT_STATUS_DELIVERED" }],
    });
    render(<Shipments />);
    const table = await screen.findByRole("table");
    const row = within(table).getByText("Delivered").closest("tr")!;
    expect(row.querySelector(".row-actions button")).toBeNull();
  });

  it("cancels a shipment", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [PENDING] });
    const cancelShipment = vi.spyOn(api, "cancelShipment").mockResolvedValue({ ...PENDING, status: "SHIPMENT_STATUS_CANCELLED" });
    render(<Shipments />);
    await screen.findByRole("table");

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(cancelShipment).toHaveBeenCalledWith("ship_1234567890", "Cancelled from admin");
    expect(await screen.findByText("Shipment #ship_123 → Cancelled")).toBeInTheDocument();
  });

  it("shows the API's reason when an action fails", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [PENDING] });
    vi.spyOn(api, "shipShipment").mockRejectedValue(
      new ApiRequestError({ status: 409, code: "CONFLICT", reason: "ALREADY_SHIPPED" }),
    );
    render(<Shipments />);
    await screen.findByRole("table");

    await user.click(screen.getByRole("button", { name: "Ship" }));

    expect(await screen.findByText("ALREADY_SHIPPED")).toBeInTheDocument();
  });

  it("refetches when the status filter changes", async () => {
    const user = userEvent.setup();
    const listShipments = vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [] });
    render(<Shipments />);
    await screen.findByText("No shipments.");
    expect(listShipments).toHaveBeenCalledWith({ status: "" });

    await user.selectOptions(screen.getByRole("combobox"), "DELIVERED");
    await waitFor(() => expect(listShipments).toHaveBeenLastCalledWith({ status: "DELIVERED" }));
  });

  it("refetches when Refresh is clicked", async () => {
    const user = userEvent.setup();
    const listShipments = vi.spyOn(api, "listShipments").mockResolvedValue({ shipments: [] });
    render(<Shipments />);
    await screen.findByText("No shipments.");
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(listShipments).toHaveBeenCalledTimes(2));
  });
});
