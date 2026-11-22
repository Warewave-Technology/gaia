-- Schema version 13 (T27 step 1b, ADR-007 C12 and C14).
-- A VM status change records the reason codes of the status it left and of the one it went to, so a notification
-- can tell an OK <-> WARNING change whose only reasons are proven_rpo_over_target or proof_too_old (C12: not sent at
-- once on a policy that cannot always be OK, counted in the daily report). Changes recorded before have none.
ALTER TABLE state_changes ADD COLUMN from_codes TEXT NOT NULL DEFAULT '';
ALTER TABLE state_changes ADD COLUMN to_codes TEXT NOT NULL DEFAULT '';
-- A ledger check is incremental (the entries since the last check and the link to it) or full (C14); every check
-- before this version was full.
ALTER TABLE ledger_checks ADD COLUMN kind TEXT NOT NULL DEFAULT 'full';
