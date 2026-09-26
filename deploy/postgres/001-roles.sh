#!/bin/bash
set -euo pipefail

: "${WALLET_MIGRATOR_PASSWORD:?WALLET_MIGRATOR_PASSWORD is required}"
: "${WALLET_APP_PASSWORD:?WALLET_APP_PASSWORD is required}"

psql -v ON_ERROR_STOP=1 \
     -v migrator_password="$WALLET_MIGRATOR_PASSWORD" \
     -v app_password="$WALLET_APP_PASSWORD" \
     -v dbname="$POSTGRES_DB" \
     --username "$POSTGRES_USER" \
     --dbname "$POSTGRES_DB" <<'EOSQL'
CREATE ROLE wallet_migrator LOGIN PASSWORD :'migrator_password';
CREATE ROLE wallet_app      LOGIN PASSWORD :'app_password';

GRANT CONNECT ON DATABASE :"dbname" TO wallet_migrator, wallet_app;

GRANT ALL   ON SCHEMA public TO wallet_migrator;
GRANT USAGE ON SCHEMA public TO wallet_app;

ALTER DEFAULT PRIVILEGES FOR ROLE wallet_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE ON TABLES TO wallet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE wallet_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO wallet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE wallet_migrator IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO wallet_app;
EOSQL
