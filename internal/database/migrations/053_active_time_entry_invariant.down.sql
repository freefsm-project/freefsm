-- Reconciled time entries are historical records; rollback only removes the invariant.
DROP INDEX IF EXISTS time_entries_one_active_per_user;
