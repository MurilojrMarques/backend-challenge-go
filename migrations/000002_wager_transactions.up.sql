CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    kind                              TEXT        NOT NULL,
    status                            TEXT        NOT NULL,
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    amount_units                      BIGINT      NOT NULL,
    currency                          TEXT        NOT NULL,
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    resolved_reference_id             UUID        REFERENCES wager_transactions (id),
    failure_code                      TEXT,
    balance_after_units               BIGINT,
    reference_attempts                INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at                   TIMESTAMPTZ,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    CONSTRAINT wager_kind_known
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_status_known
        CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wager_amount_non_negative
        CHECK (amount_units >= 0),
    CONSTRAINT wager_zero_amount_policy
        CHECK ((kind = 'LOSS') = (amount_units = 0)),

    CONSTRAINT wager_internal_has_no_external_metadata
        CHECK ((kind = 'OPENING') = (
            provider_id IS NULL
            AND external_transaction_id IS NULL
            AND idempotency_key IS NULL
            AND payload_hash IS NULL
            AND round_id IS NULL
            AND game_id IS NULL
            AND reference_external_transaction_id IS NULL
        )),
    CONSTRAINT wager_external_has_all_metadata
        CHECK (kind = 'OPENING' OR (
            provider_id IS NOT NULL
            AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL
            AND game_id IS NOT NULL
        )),
    CONSTRAINT wager_opening_is_processed
        CHECK (kind <> 'OPENING' OR status = 'PROCESSED'),
    CONSTRAINT wager_field_lengths
        CHECK (
            length(coalesce(provider_id, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(external_transaction_id, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(idempotency_key, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(payload_hash, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(round_id, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(game_id, 'x')) BETWEEN 1 AND 128
            AND length(coalesce(reference_external_transaction_id, 'x')) BETWEEN 1 AND 128
        ),

    CONSTRAINT wager_reversal_requires_reference
        CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_reference_only_where_allowed
        CHECK (kind IN ('WIN', 'REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NULL),
    CONSTRAINT wager_no_self_reference
        CHECK (reference_external_transaction_id IS DISTINCT FROM external_transaction_id),
    CONSTRAINT wager_no_self_resolution
        CHECK (resolved_reference_id IS DISTINCT FROM id),
    CONSTRAINT wager_reference_state_requires_reference
        CHECK (reference_external_transaction_id IS NOT NULL OR (
            resolved_reference_id IS NULL
            AND status <> 'PENDING_REFERENCE'
            AND reference_attempts = 0
        )),
    CONSTRAINT wager_reference_attempts_non_negative
        CHECK (reference_attempts >= 0),
    CONSTRAINT wager_pending_reference_is_scheduled
        CHECK ((status = 'PENDING_REFERENCE') = (next_attempt_at IS NOT NULL)),

    CONSTRAINT wager_terminal_has_completed_at
        CHECK ((status IN ('PROCESSED', 'REJECTED', 'FAILED')) = (completed_at IS NOT NULL)),
    CONSTRAINT wager_processed_result
        CHECK (status <> 'PROCESSED' OR (
            balance_after_units IS NOT NULL
            AND failure_code IS NULL
            AND (reference_external_transaction_id IS NULL OR resolved_reference_id IS NOT NULL)
        )),
    CONSTRAINT wager_rejected_result
        CHECK (status <> 'REJECTED' OR (
            failure_code IS NOT NULL
            AND failure_code <> 'PERMANENT_FAILURE'
        )),
    CONSTRAINT wager_failed_result
        CHECK (status <> 'FAILED' OR (
            failure_code = 'PERMANENT_FAILURE'
            AND balance_after_units IS NULL
        )),
    CONSTRAINT wager_non_terminal_has_no_result
        CHECK (status IN ('PROCESSED', 'REJECTED', 'FAILED') OR (
            failure_code IS NULL
            AND balance_after_units IS NULL
        )),
    CONSTRAINT wager_balance_after_non_negative
        CHECK (balance_after_units IS NULL OR balance_after_units >= 0),

    CONSTRAINT wager_timeline
        CHECK (updated_at >= created_at AND (completed_at IS NULL OR completed_at >= created_at))
);

CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_transactions_provider_external_key
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE provider_id IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_idempotency_key
    ON wager_transactions (provider_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_one_reversal_per_reference
    ON wager_transactions (resolved_reference_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wager_transactions_pending_reference_schedule
    ON wager_transactions (next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';
