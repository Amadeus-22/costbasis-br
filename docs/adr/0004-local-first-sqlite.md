# 0004. Local-first storage in SQLite

- Status: accepted
- Date: 2026-09-29

## Context

The data is a person's complete trading history: what they own, where, and
what they paid. Users are one person at a time, on their own machine. The
sibling projects use Postgres because they are multi-user services. This one
is not.

## Decision

- Store transactions in one SQLite file. It lives in `$XDG_DATA_HOME/costbasis/`
  (default `~/.local/share/costbasis/`), or at `COSTBASIS_DB`. The file is
  created with mode `0600` before SQLite opens it, and SQLite gives its
  `-wal`/`-shm` files the same mode.
- Use `modernc.org/sqlite`, a pure-Go driver. The binary builds with
  `CGO_ENABLED=0`, cross-compiles, and runs in a distroless image.
- Store only imported transactions, never computed results. Positions and
  reports are recomputed from the full history on every request, which is
  fast at personal scale. A cache cannot go stale, and a changed ruleset or a
  late import is reflected at once.
- Each transaction's ID is a deterministic fingerprint. It is the exchange's
  trade ID when there is one, otherwise a hash of every economic field plus the
  row's occurrence number within the file. Inserts use
  `ON CONFLICT (id) DO NOTHING`, so re-importing a file is a no-op. An
  insertion sequence column breaks ties between equal timestamps.
- Migrations are an append-only list tracked with `PRAGMA user_version`. A
  database newer than the binary is refused.
- The HTTP API binds to `127.0.0.1` by default and has no authentication.

## Consequences

- There is no server, account or network dependency. Backing up means copying
  one file.
- One writer at a time. The store uses a single connection, which is enough
  for one user.
- Two exports that overlap but come from different exchanges are not
  deduplicated against each other. An own-wallet transfer appears once per
  side, which is correct.
- Rows without an exchange ID can only be deduplicated by content. Two
  genuinely identical fills in the same second in the same file stay distinct
  (occurrence number). The same fills split across two overlapping exports may
  be counted once too few or too many. The README documents this for Binance,
  whose export has no trade ID.
- Multi-user or hosted use would need authentication and probably Postgres.
  That is out of scope (see the non-goals in ARCHITECTURE.md).
