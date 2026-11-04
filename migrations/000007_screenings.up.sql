-- Migration 000007: screening persistence (spec §7.14 + plan §3.3/§3.7,
-- phase 5). screening_results follows the spec column set verbatim plus
-- `request` (canonical request, JSONB) and `result` (the §9.7 response
-- body, type JSON — NOT jsonb: jsonb re-normalizes key order, whitespace
-- and numerics, which would break the byte-faithful replay contract of
-- GET /screenings/{id}).
--
-- screening_requests is the claim-first idempotency table:
-- (client_id, idempotency_key) is UNIQUE; screening_result_id IS NULL
-- marks a claim in-flight (or orphaned by a dead owner — stale claims
-- are takeover-eligible). request_hash is sha256 over the CANONICAL
-- request (resolved network, normalized address, defaulted options),
-- never raw body bytes.

CREATE TABLE screening_results (
    id UUID PRIMARY KEY,

    wallet_id BIGINT NOT NULL REFERENCES wallets(id),

    risk_score SMALLINT,
    risk_level VARCHAR(30) NOT NULL,
    screening_status VARCHAR(30) NOT NULL,

    scoring_policy_version VARCHAR(50) NOT NULL,

    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    provider_status JSONB NOT NULL DEFAULT '{}'::jsonb,

    screened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    request JSONB NOT NULL DEFAULT '{}'::jsonb,
    result JSON NOT NULL,

    CHECK (
        risk_score BETWEEN 0 AND 100
        OR risk_score IS NULL
    ),
    CHECK (risk_level IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL', 'UNKNOWN')),
    CHECK (screening_status IN (
        'COMPLETED', 'PARTIAL', 'INSUFFICIENT_DATA', 'FAILED'))
);

CREATE INDEX idx_screening_results_wallet
ON screening_results(wallet_id, screened_at DESC);

CREATE TABLE screening_requests (
    id BIGSERIAL PRIMARY KEY,
    client_id BIGINT NOT NULL REFERENCES api_clients(id),
    idempotency_key TEXT NOT NULL
        CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    request_hash TEXT NOT NULL,
    screening_result_id UUID REFERENCES screening_results(id),
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(client_id, idempotency_key)
);
