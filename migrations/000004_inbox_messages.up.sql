CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions (id),
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ,

    CONSTRAINT inbox_messages_pkey        PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_field_lengths        CHECK (
        length(consumer_name) BETWEEN 1 AND 128
        AND length(message_id) BETWEEN 1 AND 128
        AND length(payload_hash) BETWEEN 1 AND 128
    ),
    CONSTRAINT inbox_timeline             CHECK (completed_at IS NULL OR completed_at >= received_at)
);

CREATE INDEX inbox_messages_incomplete
    ON inbox_messages (received_at)
    WHERE completed_at IS NULL;
