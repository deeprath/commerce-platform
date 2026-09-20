# Product requirements

> **Reconstructed, not original.** This project was built design-first from
> [`ARCHITECTURE.md`](ARCHITECTURE.md); there was never a separate PRD. This file states the
> product intent that the built system actually satisfies, so that a change can be checked against a
> requirement rather than against a diagram. Where it disagrees with the code, the code wins and this
> file is wrong — fix it here.

Last reviewed: 2026-09-20.

---

## 1. What this product is

A multi-seller e-commerce platform: shoppers browse a catalog, buy from one or more independent shops
in a single order, and track it to delivery. Sellers run their own shop — listings, staff, payouts.
Operators run the platform — catalog, orders, returns, shipments, shop approval.

It is a **portfolio/reference implementation**. The requirement bar is "behaves correctly under
partial failure and is operable by two people", not "has every feature a commercial platform has".

## 2. Personas

| Persona | Keycloak role | Surface | Cares about |
|---|---|---|---|
| **Shopper** | `customer` | `web/storefront` | Finding a product, a checkout that doesn't double-charge, knowing where the order is |
| **Seller** | `customer` + shop owner (OpenFGA `shop#owner`) | `web/storefront` `/seller/*` | Listing products, seeing what sold, getting paid |
| **Shop staff** | OpenFGA `shop#staff` | `web/storefront` `/seller/*` | Same as seller, minus ownership |
| **Operator (CSR / managers)** | `csr`, `catalog_manager`, `order_manager`, `finance`, `shop_admin`, … | `web/admin` | Fixing a stuck order, deciding a return, approving a shop, marking a payout paid |
| **Platform admin** | `platform_admin` | Keycloak + admin SPA | Roles, config |

Anonymous browsing is a first-class case: search and product detail require no token.

## 3. Capabilities and their requirements

### 3.1 Browse and discovery
- A shopper can browse and filter a catalog, autocomplete a query, and open a product detail page,
  **without signing in**.
- A product detail page shows price, availability, reviews, and — for a marketplace listing — who
  sells it, with a link to that shop's page.
- Search results are **eventually consistent** with the catalog. A newly created product becoming
  findable a few seconds later is acceptable; a product detail page serving stale canonical data is not.
- A shop page lists only `ACTIVE` shops and their active listings.

### 3.2 Cart
- A cart survives a browser reload and merges on login.
- A cart is **disposable**: losing every cart is recoverable by the shopper and therefore acceptable.
  Losing an order is not.

### 3.3 Checkout
- A checkout quotes price, reserves stock, and creates a payment **before** it returns an order.
- A checkout is **idempotent**: a retried request with the same key must not place a second order.
- Every failure path compensates — a reservation is released, a payment is voided — and every
  compensation is safe to run twice.
- Payment confirmation is **asynchronous**. The client is told the order is pending and learns the
  outcome later; it must never require the browser to stay open to complete.
- Stock is reclaimed by reservation TTL (default 15 min) even if compensation fails.

### 3.4 Orders, returns, sharing
- A shopper can list and open their own orders, request a return on a delivered one, and share a
  single order with another user (read-only, revocable).
- An order reaches `FULFILLED` only once **every** shop's shipment in it has been delivered.
- An operator can list any order, decide a return, and drive shipments.

### 3.5 Marketplace
- A user can create one shop. A shop starts `PENDING_REVIEW` and is activated or suspended by an
  operator (`shop_admin`).
- A shop owner can grant and revoke staff; staff get the owner's product and payout access, not
  ownership.
- A confirmed order's lines are grouped by shop into one shipment per shop and one payout per shop.
- Payouts are **100% passthrough** — no platform commission in this version.

### 3.6 Operations
- Reviews are verified-purchase only, published on create, and moderatable.
- Notifications are transactional and event-driven, delivered through a sandbox channel.
- Analytics is an OLAP sink (funnel + revenue + clickstream), never on a request path.

## 4. Authorization requirements

Three layers, in this order, and the order is the requirement:

1. **Realm role** (`pkg/auth`, JWT verified per service against Keycloak JWKS).
2. **Ownership** (`owner_id == principal.Subject`, enforced in the repository, never role-only).
3. **Relationship** (OpenFGA, `pkg/fga`) — consulted **only after** 1 and 2 have denied.

Consequence, and it is deliberate: an OpenFGA outage fails delegated access **closed** and never
blocks a resource's own owner.

The edge check (`ext-authz`) is a load shedder, not a gate. A service that skips `grpcx.WithAuth` is
unprotected regardless of what the edge does.

## 5. Non-functional requirements

| Requirement | Target | Where enforced |
|---|---|---|
| Availability SLO | Request-availability SLO with multi-window multi-burn-rate alerts | `observability/prometheus/rules/` (ADR-024) |
| Checkout correctness | No double order, no oversell, no lost payment event | Saga + outbox + idempotency keys |
| Event durability | No "order persisted but event lost" | Transactional outbox, at-least-once + idempotent consumers |
| Poison-message tolerance | One bad record must not wedge a consumer group | Bounded retry then park on `<topic>.dlq` |
| Secrets | None in Git, images, or `values.yaml` | Gitleaks + External Secrets |
| PCI scope | SAQ-A — the platform never sees a PAN | PSP tokenization |
| Horizontal scale | Every service stateless; scale = more pods | KEDA `ScaledObject`s |
| Test bar | Integration tests run against real dependencies (testcontainers) | `task test` |

## 6. Explicit non-goals

- Multi-region **active-active** (multi-zone only).
- A mobile app — the storefront is responsive web.
- Platform commission, seller invoicing, or tax remittance.
- A service mesh in local compose ([`ARCHITECTURE.md`](ARCHITECTURE.md) — deliberate gap).
- Real payment, carrier, or notification providers; all three are sandboxed.

## 7. Done means

A capability is done when: the proto contract is in `proto/`, the service enforces the three authz
layers, failure paths are compensated and tested against a real Postgres, the BFF exposes it, a SPA
uses it, and CI (lint · unit · integration · build · buf-breaking · security) is green.
