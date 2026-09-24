CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      TEXT        NOT NULL,
    balance_units BIGINT      NOT NULL,
    version       BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_key   UNIQUE (player_id, currency),
    CONSTRAINT wallets_currency_iso4217      CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wallets_balance_non_negative  CHECK (balance_units >= 0),
    CONSTRAINT wallets_version_positive      CHECK (version >= 1),
    CONSTRAINT wallets_timeline              CHECK (updated_at >= created_at)
);
