-- Migration 000006: live provider observations audit log (spec §4.6, phase 4).
-- Append-only: every provider fetch attempt persists the verbatim payload,
-- the literal chain token sent upstream, the fetch status and timestamp.
-- Retention keeps the newest 25 rows per (source_id, wallet_id), pruned
-- inside the insert transaction by the caller — concurrent writers may
-- transiently exceed the bound by one row; the next insert self-heals.

CREATE TABLE provider_observations (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    wallet_id BIGINT NOT NULL REFERENCES wallets(id),
    requested_chain TEXT NOT NULL,          -- literal chain token sent upstream (§4.6)
    status VARCHAR(20) NOT NULL
        CHECK (status IN ('OK','DEGRADED','RATE_LIMITED','TIMEOUT',
                          'UPSTREAM_5XX','CLIENT_4XX','MALFORMED',
                          'CIRCUIT_OPEN','DISABLED','TRANSPORT','ERROR')),
    http_status INT,
    latency_ms INT,
    payload JSONB NOT NULL,
    provider_score DOUBLE PRECISION,        -- NULL means absent; 0 is data, never a verdict
    error JSONB,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_provider_observations_latest
ON provider_observations(source_id, wallet_id, fetched_at DESC);

-- On-demand provider sources (source_type 'api': live lookups, not
-- file/batch artifacts like the existing 'snapshot' sources).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('goplus',     'GoPlus Security',        'api', 'https://gopluslabs.io/'),
    ('publicaml',  'PublicAML',              'api', 'https://publicaml.org/'),
    ('chainabuse', 'Chainabuse (TRM Labs)',  'api', 'https://chainabuse.com/');
