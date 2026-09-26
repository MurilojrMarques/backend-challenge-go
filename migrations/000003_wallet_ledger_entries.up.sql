CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL REFERENCES wallets (id),
    transaction_id       UUID        NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT        NOT NULL,
    amount_units         BIGINT      NOT NULL,
    currency             TEXT        NOT NULL,
    balance_before_units BIGINT      NOT NULL,
    balance_after_units  BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_wallet_transaction_key    UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_direction_known           CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_currency_iso4217          CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT ledger_amount_positive           CHECK (amount_units > 0),
    CONSTRAINT ledger_balances_non_negative     CHECK (balance_before_units >= 0 AND balance_after_units >= 0),
    CONSTRAINT ledger_balances_are_consistent   CHECK (
        balance_after_units = balance_before_units
            + CASE direction WHEN 'CREDIT' THEN amount_units ELSE -amount_units END
    )
);

CREATE INDEX wallet_ledger_entries_by_wallet
    ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE FUNCTION wallet_ledger_entries_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only'
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER wallet_ledger_entries_immutable_rows
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_reject_mutation();

CREATE TRIGGER wallet_ledger_entries_immutable_table
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_ledger_entries_reject_mutation();

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        REVOKE UPDATE, DELETE, TRUNCATE ON wallet_ledger_entries FROM wallet_app;
        REVOKE INSERT, UPDATE ON schema_migrations FROM wallet_app;
    END IF;
END;
$$;
