# Design — client surface and API shape

How the browser-facing half of the platform is put together: the BFF's REST surface, the two SPAs,
the auth flow, and the UI states that the asynchronous backend forces on the front end.

Backend/system design lives in [`ARCHITECTURE.md`](ARCHITECTURE.md) and
[`ARCHITECTURE.md`](ARCHITECTURE.md). Conventions and prohibitions are in
[`RULES.md`](RULES.md).

---

## 1. The boundary

```
web/storefront (:5173)  ─┐
                         ├─> Envoy edge ─> services/bff ─gRPC─> 11 domain services
web/admin      (:5174)  ─┘                  (Echo, no DB)
```

The BFF exists for two reasons and does nothing else:

- **Aggregation.** One endpoint fans out to 3–4 services concurrently and returns one JSON document.
  A product-detail response is `catalog.GetProduct` + `pricing.QuotePrice` + `review.ListReviews` +
  `inventory.CheckAvailability` composed server-side, so the SPA makes one request, not four.
- **The auth-cookie boundary.** Tokens never reach JavaScript (see §3).

Design consequence: **a new screen usually needs a new BFF endpoint, not new SPA fan-out.** If a page
is making three calls and stitching them, that stitching belongs in `services/bff/internal/api/`.

## 2. REST surface (`services/bff/internal/api/`)

Grouped by who may call it — the grouping *is* the authorization design.

| Group | Prefix | Gate |
|---|---|---|
| Public | `/api/v1/catalog/*`, `/search/autocomplete`, `/shops/:slug[/products]` | none — anonymous browse is a requirement |
| Telemetry | `POST /api/v1/events` | none (clickstream beacon) |
| Session | `POST /api/v1/auth/{login,logout}` | none |
| Shopper | `/cart/*`, `/checkout*`, `/orders*` | cookie session + owner scoping |
| Seller | `/seller/*` | cookie session + OpenFGA `shop#owner`/`shop#staff` |
| Operator | `/admin/*` | cookie session + realm role |

Shape rules: plural nouns, `:slug` for public identity and `:id` for internal identity, state
transitions as `POST /<resource>/:id/<verb>` (`/archive`, `/ship`, `/mark-paid`) rather than a PATCH
with a magic status field. Errors carry the `pkg/errs` mapping — a handler never invents a status.

## 3. Auth flow

There is **no identity service and no login SPA** — both are in the design doc, neither was built.
Login lives in `services/bff/internal/auth/broker.go`:

1. SPA posts credentials to `POST /api/v1/auth/login`.
2. The BFF does the Resource-Owner-Password grant against Keycloak.
3. The BFF sets an **httpOnly, Secure, SameSite** cookie and refreshes it server-side.
4. The SPA never sees a token; `localStorage` holds no credential.

Each app has its own plain login form (`web/*/src/pages/Login.tsx`). A logout must invalidate the
refresh token, not only clear the cookie — that was a real bug (`d77b1ce`).

## 4. Storefront (`web/storefront`)

React + TypeScript + Vite. Pages: `Browse`, `Product`, `Shop`, `Cart`, `Checkout`, `Orders`,
`Login`, and the seller dashboard at `/seller/*` (`SellerDashboard`, `SellerProducts`, `SellerStaff`,
`SellerPayouts`).

- **Cart is client context over a Redis-backed API** (`cart-context.ts`, `cart.tsx`). It is allowed to
  be lossy; orders are not.
- **The seller dashboard added no backend.** It is four pages over the existing `/seller/*` BFF
  routes — the marketplace slices landed API-first, and the UI was the last slice (ADR-044).
- `track.ts` is the clickstream beacon: fire-and-forget to `POST /api/v1/events`, never blocking a
  render or a navigation.
- `api.ts` is the single place that talks HTTP. Pages don't call `fetch`.

## 5. Admin (`web/admin`)

Separate SPA on its own hostname (ADR-023), not a route inside the storefront — so it can be
restricted by a source-IP `AuthorizationPolicy` at the gateway. Pages: `Orders`, `OrderDetail`,
`Returns`, `Shipments`, `Products`, `Login`. Same `api.ts` discipline; shared UI is duplicated rather
than extracted into a package, deliberately — two small apps beat one shared design system here.

## 6. Designing for an asynchronous backend

The three states that the architecture forces into the UI. Every new screen has to answer them:

1. **Checkout returns `PENDING_PAYMENT`, not a confirmed order.** Authorization arrives later over
   Kafka. The storefront polls and gives up after 15 attempts, showing the order as still pending —
   *not* as failed. Never render a pending order as an error; it is very likely about to confirm.
2. **Search is eventually consistent with the catalog.** A just-created product is not immediately
   findable. Seller-facing lists read the catalog (`ListShopProducts`), not search, for exactly this
   reason. Don't "fix" a missing product by adding a retry loop in the UI.
3. **Anything marketplace can degrade.** The BFF attaches `sold_by` to a product detail response and
   drops it gracefully if the shop lookup fails. The PDP renders without the "Sold by" line; it does
   not fail the page.

## 7. Front-end conventions

- **jsdom, not happy-dom**, in `vite.config.ts` — happy-dom silently no-ops form-submit tests.
- Type submit handlers as `SubmitEvent`, not the deprecated `FormEvent` (`41b5406`).
- Every page has a colocated `*.test.tsx`; coverage feeds SonarCloud via `npm run test:coverage`.
- Plain CSS in `styles.css` per app. No CSS-in-JS, no component library.
- Local login: `testuser` / `testuser123`. Admin needs an operator role assigned in Keycloak —
  see [`SETUP.md`](SETUP.md).
