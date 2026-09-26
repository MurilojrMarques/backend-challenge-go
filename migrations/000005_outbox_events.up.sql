CREATE TABLE outbox_events (
    event_id        UUID        PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT,

    CONSTRAINT outbox_event_type_known
        CHECK (event_type IN (
            'WagerTransactionProcessed',
            'WagerTransactionRejected',
            'WagerTransactionPendingReference',
            'WalletBalanceChanged'
        )),
    CONSTRAINT outbox_attempts_non_negative  CHECK (attempts >= 0),
    CONSTRAINT outbox_lock_is_complete       CHECK ((locked_by IS NULL) = (locked_until IS NULL)),
    CONSTRAINT outbox_published_is_unlocked  CHECK (published_at IS NULL OR locked_by IS NULL),
    CONSTRAINT outbox_payload_is_object      CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_payload_matches_row    CHECK (
        (payload ->> 'eventId')::uuid IS NOT DISTINCT FROM event_id
        AND payload ->> 'eventType' IS NOT DISTINCT FROM event_type
        AND (payload ->> 'aggregateId')::uuid IS NOT DISTINCT FROM aggregate_id
    )
);

CREATE INDEX outbox_events_pending
    ON outbox_events (occurred_at, event_id)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_pending_by_aggregate
    ON outbox_events (aggregate_id, occurred_at, event_id)
    WHERE published_at IS NULL;
