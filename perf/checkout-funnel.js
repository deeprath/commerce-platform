// k6 load test — the storefront checkout funnel.
//
// Two scenarios run together against the BFF's public REST surface:
//   browse   — anonymous product browsing (list, autocomplete, PDP)
//   checkout — the signed-in funnel: add to cart -> create order -> confirm
//              payment (the order saga runs downstream)
//
// The funnel models shoppers who are already signed in: setup() authenticates
// once and every checkout VU reuses that bearer token. Auth throughput is a
// separate concern and deliberately out of scope here.
//
// Target is the BFF directly (:8088) so the numbers reflect the application,
// not the edge proxy's rate-limiter. Point BASE_URL at the envoy edge (:8080)
// to include ratelimit + ext-authz.
//
//   k6 run perf/checkout-funnel.js
//   BASE_URL=http://localhost:8080/api/v1 BROWSE_VUS=40 CHECKOUT_RPS=8 k6 run perf/checkout-funnel.js
//
// Tunables (env): BASE_URL, K6_USERNAME, K6_PASSWORD, RAMP, HOLD,
//                 BROWSE_VUS, CHECKOUT_RPS, CHECKOUT_MAX_VUS.

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

const BASE = (__ENV.BASE_URL || 'http://localhost:8088/api/v1').replace(/\/+$/, '');
const USERNAME = __ENV.K6_USERNAME || 'testuser';
const PASSWORD = __ENV.K6_PASSWORD || 'testuser123';

const RAMP = __ENV.RAMP || '20s';
const HOLD = __ENV.HOLD || '40s';
const BROWSE_VUS = Number(__ENV.BROWSE_VUS || 20);
const CHECKOUT_RPS = Number(__ENV.CHECKOUT_RPS || 3);
const CHECKOUT_MAX_VUS = Number(__ENV.CHECKOUT_MAX_VUS || 40);

// A sold-out product answers CreateOrder with 409 FAILED_PRECONDITION. That is
// a real funnel outcome, not a platform error — keep it out of http_req_failed.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 299 }, 409));

const checkoutFlow = new Trend('checkout_flow_duration', true);
const checkoutOK = new Rate('checkout_flow_success');
const outOfStock = new Counter('checkout_out_of_stock');

export const options = {
  scenarios: {
    browse: {
      executor: 'ramping-vus',
      exec: 'browse',
      startVUs: 1,
      stages: [
        { duration: RAMP, target: BROWSE_VUS },
        { duration: HOLD, target: BROWSE_VUS },
        { duration: '10s', target: 0 },
      ],
      gracefulRampDown: '10s',
      tags: { scenario: 'browse' },
    },
    checkout: {
      executor: 'ramping-arrival-rate',
      exec: 'checkout',
      startRate: 1,
      timeUnit: '1s',
      preAllocatedVUs: 10,
      maxVUs: CHECKOUT_MAX_VUS,
      stages: [
        { duration: RAMP, target: CHECKOUT_RPS },
        { duration: HOLD, target: CHECKOUT_RPS },
        { duration: '10s', target: 0 },
      ],
      tags: { scenario: 'checkout' },
    },
  },
  thresholds: {
    // Platform availability — genuine transport / 5xx failures only.
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    // Browsing is cache-friendly and must stay snappy.
    'http_req_duration{scenario:browse}': ['p(95)<400', 'p(99)<900'],
    // End-to-end funnel latency (add to cart -> create order -> confirm payment).
    checkout_flow_duration: ['p(95)<2500'],
    checkout_flow_success: ['rate>0.95'],
  },
};

const SHIP_TO = {
  full_name: 'Load Test',
  line1: '1 Perf Way',
  city: 'Portland',
  region: 'OR',
  postal_code: '97201',
  country_code: 'US',
};
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

function pick(xs) {
  return xs[Math.floor(Math.random() * xs.length)];
}

export function setup() {
  const res = http.get(`${BASE}/catalog/products?page_size=20`, { tags: { name: 'catalog_products' } });
  if (res.status !== 200) {
    throw new Error(`catalog not ready at ${BASE}: HTTP ${res.status}`);
  }
  let hits = [];
  try {
    hits = res.json('hits') || [];
  } catch (_) {
    hits = [];
  }
  const products = hits
    .map((h) => ({ id: h.product_id, slug: h.slug }))
    .filter((p) => p.id && p.slug);
  if (products.length === 0) {
    throw new Error(`no seeded products at ${BASE} — start the stack and let the catalog seed`);
  }

  const login = http.post(
    `${BASE}/auth/login`,
    JSON.stringify({ username: USERNAME, password: PASSWORD }),
    JSON_HEADERS,
  );
  const token = ((login.cookies.access_token || [])[0] || {}).value;
  if (login.status !== 200 || !token) {
    throw new Error(`login failed for ${USERNAME}: HTTP ${login.status}`);
  }
  return { products, token };
}

export function browse(data) {
  group('browse', () => {
    const list = http.get(`${BASE}/catalog/products?page_size=20`, { tags: { name: 'catalog_products' } });
    check(list, { 'products list 200': (r) => r.status === 200 });

    const ac = http.get(`${BASE}/search/autocomplete?q=${pick(['de', 'no', 'wa', 'st'])}`, {
      tags: { name: 'autocomplete' },
    });
    check(ac, { 'autocomplete 200': (r) => r.status === 200 });

    const pdp = http.get(`${BASE}/catalog/products/${pick(data.products).slug}`, {
      tags: { name: 'product_detail' },
    });
    check(pdp, { 'product detail 200': (r) => r.status === 200 });
  });
  sleep(Math.random() * 2 + 0.5);
}

export function checkout(data) {
  // Signed-in shopper with a fresh cart each iteration. The cart is keyed by a
  // cookie the BFF mints on first use, so a clean jar == a new cart.
  const jar = http.cookieJar();
  jar.clear(BASE);
  const params = {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${data.token}` },
  };
  const started = Date.now();
  let ok = false;

  group('checkout', () => {
    const add = http.post(
      `${BASE}/cart/items`,
      JSON.stringify({ product_id: pick(data.products).id, quantity: 1 }),
      { ...params, tags: { name: 'cart_add' } },
    );
    if (!check(add, { 'cart add 200': (r) => r.status === 200 })) return;

    const order = http.post(
      `${BASE}/checkout`,
      JSON.stringify({ currency_code: 'USD', payment_method_token: 'pm_card_ok', ship_to: SHIP_TO }),
      { ...params, tags: { name: 'checkout' } },
    );
    if (order.status === 409) {
      outOfStock.add(1);
      return;
    }
    if (!check(order, { 'checkout 201': (r) => r.status === 201 })) return;

    let paymentId = '';
    try {
      paymentId = order.json('payment_id') || '';
    } catch (_) {
      paymentId = '';
    }
    if (!check(paymentId, { 'order returned a payment_id': (v) => v !== '' })) return;

    const confirm = http.post(
      `${BASE}/checkout/confirm`,
      JSON.stringify({ payment_id: paymentId, outcome: 'authorize' }),
      { ...params, tags: { name: 'checkout_confirm' } },
    );
    ok = check(confirm, { 'confirm 200': (r) => r.status === 200 });
  });

  checkoutFlow.add(Date.now() - started);
  checkoutOK.add(ok);
  sleep(1);
}
