-- =============================================================================
-- Management schema — everything the management API owns.
-- Kept separate from the pdns schema so PowerDNS updates never touch it.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS mgmt;
SET search_path TO mgmt, public;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Users -----------------------------------------------------------------------
CREATE TABLE mgmt.users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email         VARCHAR(255) NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role          VARCHAR(32) NOT NULL CHECK (role IN ('admin','operator','viewer')),
  disabled      BOOLEAN NOT NULL DEFAULT FALSE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ
);

-- API keys --------------------------------------------------------------------
-- Only the hash is stored. Plaintext token is returned once at creation.
CREATE TABLE mgmt.api_keys (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       UUID NOT NULL REFERENCES mgmt.users(id) ON DELETE CASCADE,
  name          VARCHAR(128) NOT NULL,
  token_hash    TEXT NOT NULL UNIQUE,
  token_prefix  VARCHAR(16) NOT NULL,          -- first 8 chars, shown in UI for identification
  scopes        TEXT[] NOT NULL DEFAULT '{}',  -- e.g. {zones:read, records:write}
  disabled      BOOLEAN NOT NULL DEFAULT FALSE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at  TIMESTAMPTZ,
  expires_at    TIMESTAMPTZ
);

CREATE INDEX ON mgmt.api_keys(user_id);
CREATE INDEX ON mgmt.api_keys(token_prefix);

-- Zone metadata ---------------------------------------------------------------
-- The pdns.domains table is the source of truth for zone existence. This
-- table stores management metadata we don't want to pollute the pdns schema
-- with (ownership, tags, description).
CREATE TABLE mgmt.zone_meta (
  zone_name     VARCHAR(255) PRIMARY KEY,
  is_private    BOOLEAN NOT NULL DEFAULT TRUE,
  description   TEXT,
  tags          TEXT[] NOT NULL DEFAULT '{}',
  created_by    UUID REFERENCES mgmt.users(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Audit log -------------------------------------------------------------------
-- Append-only. Every write action against zones/records/users lands here.
CREATE TABLE mgmt.audit_log (
  id            BIGSERIAL PRIMARY KEY,
  ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor_user    UUID REFERENCES mgmt.users(id),
  actor_key     UUID REFERENCES mgmt.api_keys(id),
  actor_ip      INET,
  action        VARCHAR(64) NOT NULL,        -- zone.create, record.update, user.login, ...
  target_type   VARCHAR(32),                 -- zone | record | user | key | system
  target_id     TEXT,
  outcome       VARCHAR(16) NOT NULL,        -- success | failure | denied
  detail        JSONB
);

CREATE INDEX ON mgmt.audit_log(ts DESC);
CREATE INDEX ON mgmt.audit_log(action);
CREATE INDEX ON mgmt.audit_log(actor_user);
CREATE INDEX ON mgmt.audit_log(target_type, target_id);

-- Sessions --------------------------------------------------------------------
-- We use JWTs for stateless auth, but keep a revocation list so an admin can
-- kill a session on demand.
CREATE TABLE mgmt.revoked_tokens (
  jti           UUID PRIMARY KEY,
  revoked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at    TIMESTAMPTZ NOT NULL   -- once natural expiry passes we can GC
);

CREATE INDEX ON mgmt.revoked_tokens(expires_at);

-- Query log (optional, low-volume) --------------------------------------------
-- PowerDNS's own logging is authoritative for high-volume query telemetry.
-- This table exists so the dashboard can surface "recent queries" without
-- shelling into the container. Feed via pdns log sink or leave empty.
CREATE TABLE mgmt.recent_queries (
  id            BIGSERIAL PRIMARY KEY,
  ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
  client_ip     INET,
  qname         VARCHAR(255) NOT NULL,
  qtype         VARCHAR(10)  NOT NULL,
  rcode         VARCHAR(16),
  response_ms   INT
);

CREATE INDEX ON mgmt.recent_queries(ts DESC);
CREATE INDEX ON mgmt.recent_queries(qname);

-- updated_at trigger for zone_meta -------------------------------------------
CREATE OR REPLACE FUNCTION mgmt.touch_updated_at() RETURNS trigger AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER zone_meta_touch
  BEFORE UPDATE ON mgmt.zone_meta
  FOR EACH ROW EXECUTE FUNCTION mgmt.touch_updated_at();
