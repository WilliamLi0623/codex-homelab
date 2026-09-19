# Recovery

Bootstrap state is persisted atomically in state.json. Each stage verifies prerequisites and postconditions before marking success.

Unknown outcome is never equivalent to retry. Reconcile WebCodex, Git, GitHub, and SQLite before replaying a mutation.