-- =============================================================================
-- PowerDNS Authoritative — canonical PostgreSQL schema
-- Source: https://doc.powerdns.com/authoritative/backends/generic-postgresql.html
-- Kept verbatim so PowerDNS upgrades in place cleanly.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS pdns;
SET search_path TO pdns, public;

CREATE TABLE IF NOT EXISTS pdns.domains (
  id                    SERIAL PRIMARY KEY,
  name                  VARCHAR(255) NOT NULL,
  master                VARCHAR(128) DEFAULT NULL,
  last_check            INT DEFAULT NULL,
  type                  VARCHAR(8) NOT NULL,
  notified_serial       BIGINT DEFAULT NULL,
  account               VARCHAR(40) DEFAULT NULL,
  options               TEXT DEFAULT NULL,
  catalog               TEXT DEFAULT NULL,
  CONSTRAINT c_lowercase_name CHECK (((name)::text = LOWER((name)::text)))
);

CREATE UNIQUE INDEX IF NOT EXISTS name_index ON pdns.domains(name);
CREATE INDEX IF NOT EXISTS catalog_idx ON pdns.domains(catalog);

CREATE TABLE IF NOT EXISTS pdns.records (
  id                    BIGSERIAL PRIMARY KEY,
  domain_id             INT DEFAULT NULL REFERENCES pdns.domains(id) ON DELETE CASCADE,
  name                  VARCHAR(255) DEFAULT NULL,
  type                  VARCHAR(10) DEFAULT NULL,
  content               VARCHAR(65535) DEFAULT NULL,
  ttl                   INT DEFAULT NULL,
  prio                  INT DEFAULT NULL,
  disabled              BOOL DEFAULT 'f',
  ordername             VARCHAR(255),
  auth                  BOOL DEFAULT 't',
  CONSTRAINT c_lowercase_name CHECK (((name)::text = LOWER((name)::text)))
);

CREATE INDEX IF NOT EXISTS rec_name_index ON pdns.records(name);
CREATE INDEX IF NOT EXISTS nametype_index ON pdns.records(name, type);
CREATE INDEX IF NOT EXISTS domain_id ON pdns.records(domain_id);
CREATE INDEX IF NOT EXISTS recordorder ON pdns.records(domain_id, ordername text_pattern_ops);

CREATE TABLE IF NOT EXISTS pdns.supermasters (
  ip                    INET NOT NULL,
  nameserver            VARCHAR(255) NOT NULL,
  account               VARCHAR(40) NOT NULL,
  PRIMARY KEY (ip, nameserver)
);

CREATE TABLE IF NOT EXISTS pdns.comments (
  id                    SERIAL PRIMARY KEY,
  domain_id             INT NOT NULL REFERENCES pdns.domains(id) ON DELETE CASCADE,
  name                  VARCHAR(255) NOT NULL,
  type                  VARCHAR(10) NOT NULL,
  modified_at           INT NOT NULL,
  account               VARCHAR(40) DEFAULT NULL,
  comment               VARCHAR(65535) NOT NULL,
  CONSTRAINT c_lowercase_name CHECK (((name)::text = LOWER((name)::text)))
);

CREATE INDEX IF NOT EXISTS comments_domain_id_idx ON pdns.comments(domain_id);
CREATE INDEX IF NOT EXISTS comments_name_type_idx ON pdns.comments(name, type);
CREATE INDEX IF NOT EXISTS comments_order_idx ON pdns.comments(domain_id, modified_at);

CREATE TABLE IF NOT EXISTS pdns.domainmetadata (
  id                    SERIAL PRIMARY KEY,
  domain_id             INT REFERENCES pdns.domains(id) ON DELETE CASCADE,
  kind                  VARCHAR(32),
  content               TEXT
);

CREATE INDEX IF NOT EXISTS domainidmetaindex ON pdns.domainmetadata(domain_id);

CREATE TABLE IF NOT EXISTS pdns.cryptokeys (
  id                    SERIAL PRIMARY KEY,
  domain_id             INT REFERENCES pdns.domains(id) ON DELETE CASCADE,
  flags                 INT NOT NULL,
  active                BOOL,
  published             BOOL DEFAULT TRUE,
  content               TEXT
);

CREATE INDEX IF NOT EXISTS domainidindex ON pdns.cryptokeys(domain_id);

CREATE TABLE IF NOT EXISTS pdns.tsigkeys (
  id                    SERIAL PRIMARY KEY,
  name                  VARCHAR(255),
  algorithm             VARCHAR(50),
  secret                VARCHAR(255),
  CONSTRAINT c_lowercase_name CHECK (((name)::text = LOWER((name)::text)))
);

CREATE UNIQUE INDEX IF NOT EXISTS namealgoindex ON pdns.tsigkeys(name, algorithm);
