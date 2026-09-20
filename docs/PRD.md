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

## 3. Market approach

The two promises this platform makes, who each is made to, and what in the build actually backs it.
Nothing here is aspiration: every claim is something the code already does, and each one is followed
by what it costs — a promise with no stated cost is marketing, not a requirement.

### 3.1 To the customer

| Promise | What backs it | What it costs |
|---|---|---|
| **Look before you sign up.** Browse, search, open any product and any shop page without an account. | Anonymous browse is structural — `search` is the one service that deliberately registers no auth interceptor. | An anonymous cart is Redis with a TTL. It can vanish, and that is accepted. |
| **A double-click never costs you twice.** | Checkout is idempotent on a client key; a retry returns the same order instead of placing a second one. | The client must send and reuse a key. A client that doesn't gets the old behaviour. |
| **We never take your money and lose your order.** | Saga state is persisted *before* each side effect, and every failure path compensates — reservation released, payment voided. Events leave through a transactional outbox, so an order cannot commit without its event. | Compensation is best-effort. Stock can stay reserved until the TTL expires. |
| **"Pending" is not "failed."** Pay, close the tab, get told later. | Payment authorization arrives asynchronously over Kafka. Nothing about completing an order requires the browser to stay open. | The UI must be written to say this. A pending order rendered as an error breaks the promise even though the backend kept it. |
| **One basket, many shops, no surprises.** You are told before you pay that an order will arrive in more than one delivery. | A confirmed order's lines are grouped by shop into one shipment per shop, and the order only reaches `FULFILLED` once every shop has delivered. | A single progress bar would misrepresent this. Per-shop tracking is mandatory, not a nicety. |
| **You always know who you are buying from.** | The product page carries `sold_by` with a link to the shop, and degrades gracefully to no line rather than failing the page. | Slightly more fan-out on the hottest read path in the system. |
| **Reviews are from people who actually bought it.** | Verified-purchase index built from `order.confirmed`; a review cannot exist without a matching purchase. | Far fewer reviews, and new listings start at zero for longer. That is the trade, taken on purpose. |
| **Share a delivery, not your password.** | Order sharing is a revocable per-order grant (OpenFGA `order#viewer`), checked only after owner and operator access have been denied. | Delegated access fails closed during an OpenFGA outage. The owner is never blocked; the person they shared with can be. |
| **A return is a request, not a fight.** | RMA lives in the order aggregate; an operator decides; refunds can be partial and cumulative. | Every decision is a human one. There is no auto-approve rule. |

### 3.2 To the seller

| Promise | What backs it | What it costs |
|---|---|---|
| **You keep 100% of what you sell.** | Payouts are literal passthrough — the `payout` service sums a confirmed order's lines per shop with **no platform commission** deducted anywhere in the code. | The platform earns nothing. See §3.3 — this is the open question, not a settled position. |
| **You are paid when the order confirms, not when a monthly cycle says so.** | A `Payout` is created on `order.confirmed` — at payment authorization, ahead of delivery. | The platform carries the refund and fraud risk in the gap, and there is currently no way to claw a payout back (§3.3). |
| **Your team, without sharing your login.** | A shop owner grants and revokes `shop#staff`; staff inherit product and payout access, never ownership. | Staff access depends on OpenFGA being up. The owner's own access never does. |
| **Your listings cannot be touched by another seller.** | Per-shop ownership is enforced in the catalog itself (`products.shop_id` + `product#manager`), not by UI convention. | Platform operators can still act on any listing. Seller-proof, not operator-proof. |
| **Your shipments move at your pace.** | Fulfillment groups by shop, so one slow shop delays only its own shipment. | The *order* still waits for everyone before it reads `FULFILLED`, which a seller dashboard should not present as that seller's problem. |
| **One login, one dashboard.** No separate seller portal. | `/seller/*` lives inside the storefront on the same session and the same BFF surface. | The storefront bundle carries seller code for every shopper. |
| **You are reviewed before you are live.** | A shop opens `PENDING_REVIEW` and an operator (`shop_admin`) activates it; only `ACTIVE` shops and listings are publicly visible. | Real onboarding friction, and a human in the loop. Bought deliberately: it is what lets a buyer trust an unknown shop. |

### 3.3 Open commercial questions

These are genuinely undecided. They are listed here rather than quietly assumed, because each one
changes what the product *is*, not just how it is built.

1. **There is no revenue model.** 100% passthrough is a real differentiator and also means the
   platform earns nothing per transaction. The live options — a commission rate, a listing or
   subscription fee, promoted placement, or a margin on payment processing — are mutually visible to
   sellers and need deciding before "market" means anything. Whatever is chosen lands in `payout`.
2. **There is no payout clawback.** `Payout` has exactly two states, `PENDING` and `PAID`, and the
   service consumes `order.confirmed` and `payment.authorized` — **not** `payment.refunded` or
   `order.return_approved`. A refund or approved return after a payout is marked `PAID` has no path
   to recover the money. Paying at confirmation (above) is what makes this urgent rather than
   theoretical.
3. **One shop per user.** A seller with two brands needs two accounts. Fine for a first market,
   wrong for a serious one.
4. **Sellers get paid, but not informed.** There is a payout ledger and no seller-facing sales
   analytics — the analytics pipeline is operator-facing (ClickHouse) with no seller-scoped surface.
   A seller can see what they were paid, not what is selling.
5. **Nothing is live.** Payments, carriers, and notifications are all sandboxed. This is a reference
   implementation of a market posture, not a platform with customers on it.

## 4. Capabilities and their requirements

### 4.1 Browse and discovery
- A shopper can browse and filter a catalog, autocomplete a query, and open a product detail page,
  **without signing in**.
- A product detail page shows price, availability, reviews, and — for a marketplace listing — who
  sells it, with a link to that shop's page.
- Search results are **eventually consistent** with the catalog. A newly created product becoming
  findable a few seconds later is acceptable; a product detail page serving stale canonical data is not.
- A shop page lists only `ACTIVE` shops and their active listings.

### 4.2 Cart
- A cart survives a browser reload and merges on login.
- A cart is **disposable**: losing every cart is recoverable by the shopper and therefore acceptable.
  Losing an order is not.

### 4.3 Checkout
- A checkout quotes price, reserves stock, and creates a payment **before** it returns an order.
- A checkout is **idempotent**: a retried request with the same key must not place a second order.
- Every failure path compensates — a reservation is released, a payment is voided — and every
  compensation is safe to run twice.
- Payment confirmation is **asynchronous**. The client is told the order is pending and learns the
  outcome later; it must never require the browser to stay open to complete.
- Stock is reclaimed by reservation TTL (default 15 min) even if compensation fails.

### 4.4 Orders, returns, sharing
- A shopper can list and open their own orders, request a return on a delivered one, and share a
  single order with another user (read-only, revocable).
- An order reaches `FULFILLED` only once **every** shop's shipment in it has been delivered.
- An operator can list any order, decide a return, and drive shipments.

### 4.5 Marketplace
- A user can create one shop. A shop starts `PENDING_REVIEW` and is activated or suspended by an
  operator (`shop_admin`).
- A shop owner can grant and revoke staff; staff get the owner's product and payout access, not
  ownership.
- A confirmed order's lines are grouped by shop into one shipment per shop and one payout per shop.
- Payouts are **100% passthrough** — no platform commission in this version.

### 4.6 Operations
- Reviews are verified-purchase only, published on create, and moderatable.
- Notifications are transactional and event-driven, delivered through a sandbox channel.
- Analytics is an OLAP sink (funnel + revenue + clickstream), never on a request path.

## 5. Authorization requirements

Three layers, in this order, and the order is the requirement:

1. **Realm role** (`pkg/auth`, JWT verified per service against Keycloak JWKS).
2. **Ownership** (`owner_id == principal.Subject`, enforced in the repository, never role-only).
3. **Relationship** (OpenFGA, `pkg/fga`) — consulted **only after** 1 and 2 have denied.

Consequence, and it is deliberate: an OpenFGA outage fails delegated access **closed** and never
blocks a resource's own owner.

The edge check (`ext-authz`) is a load shedder, not a gate. A service that skips `grpcx.WithAuth` is
unprotected regardless of what the edge does.

## 6. Non-functional requirements

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

## 7. Explicit non-goals

- Multi-region **active-active** (multi-zone only).
- A mobile app — the storefront is responsive web.
- Platform commission, seller invoicing, or tax remittance. **Out of scope for this version, not settled as policy** — whether the platform ever takes a cut is §3.3's first open question.
- A service mesh in local compose ([`ARCHITECTURE.md`](ARCHITECTURE.md) — deliberate gap).
- Real payment, carrier, or notification providers; all three are sandboxed.

## 8. Done means

A capability is done when: the proto contract is in `proto/`, the service enforces the three authz
layers, failure paths are compensated and tested against a real Postgres, the BFF exposes it, a SPA
uses it, and CI (lint · unit · integration · build · buf-breaking · security) is green.
