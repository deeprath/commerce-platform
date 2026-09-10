-- One database per service (database-per-service; no cross-service joins).
-- Locally these live in one Postgres instance; in production the write-heavy
-- ones move to their own clusters with no application change.

CREATE DATABASE identity;
CREATE DATABASE catalog;
CREATE DATABASE search;
CREATE DATABASE inventory;
CREATE DATABASE cart;
CREATE DATABASE pricing;
CREATE DATABASE "order";
CREATE DATABASE payment;
CREATE DATABASE fulfillment;
CREATE DATABASE notification;
CREATE DATABASE review;
CREATE DATABASE media;
CREATE DATABASE keycloak;
CREATE DATABASE openfga; -- fine-grained authz store (OpenFGA), not a service DB

-- The compose POSTGRES_USER owns them all locally. Real environments give each
-- service its own least-privileged role.
