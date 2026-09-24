DROP TRIGGER IF EXISTS wallet_ledger_entries_immutable_table ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS wallet_ledger_entries_immutable_rows ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS wallet_ledger_entries_reject_mutation();
DROP TABLE IF EXISTS wallet_ledger_entries;
