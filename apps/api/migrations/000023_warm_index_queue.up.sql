-- Migration 000023: warm index queue — signal-prioritized background
-- deep-indexing (warm-index-queue v1, plan D2/D4/D8). Additive only.
-- warm_queue dedupes ACTIVE rows via the partial unique index; done/dead
-- rows are history and never block re-enqueue. warm_quota_ledger counts
-- real upstream calls per (provider, UTC day) — a soft daily cap.
CREATE TABLE warm_queue (
    id BIGSERIAL PRIMARY KEY,
    network_id BIGINT NOT NULL REFERENCES blockchain_networks(id),
    address TEXT NOT NULL CHECK (address ~ '^0x[0-9a-f]{40}$'),
    priority INT NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    source_screening_id UUID,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','claimed','done','dead')),
    attempts INT NOT NULL DEFAULT 0,
    claimed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_warm_queue_active
    ON warm_queue (network_id, address)
    WHERE status IN ('pending','claimed');
CREATE INDEX idx_warm_queue_pending_order
    ON warm_queue (priority DESC, id) WHERE status = 'pending';

CREATE TABLE warm_quota_ledger (
    provider_code TEXT NOT NULL,
    period_start DATE NOT NULL,
    calls INT NOT NULL DEFAULT 0,
    PRIMARY KEY (provider_code, period_start)
);
