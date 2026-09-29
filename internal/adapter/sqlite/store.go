// Package sqlite implements app.Store on a local SQLite file using the pure-Go
// modernc.org/sqlite driver (no cgo, so the binary cross-compiles and the
// container needs no C library). See docs/adr/0004-local-first-sqlite.md.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// migrations are applied in order; the index+1 is the schema version.
// Never edit a shipped migration: append a new one.
var migrations = []string{
	`CREATE TABLE transactions (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT, -- insertion order breaks time ties
		id           TEXT    NOT NULL UNIQUE,
		time_unix_ns INTEGER NOT NULL,
		exchange     TEXT    NOT NULL,
		kind         TEXT    NOT NULL,
		asset        TEXT    NOT NULL,
		qty          TEXT    NOT NULL, -- exact rational, big.Rat RatString
		quote_asset  TEXT    NOT NULL,
		quote_amount TEXT    NOT NULL,
		fee          TEXT    NOT NULL,
		fee_asset    TEXT    NOT NULL,
		external_id  TEXT    NOT NULL,
		imported_at  INTEGER NOT NULL
	);
	CREATE INDEX transactions_time ON transactions (time_unix_ns, seq);`,
}

// Store is an app.Store backed by SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	// Create the file owner-only before SQLite does: it holds private
	// financial data, and SQLite gives its -wal/-shm files the same mode.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	f.Close()
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY and is
	// plenty for a single-user tool.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		// PRAGMA does not accept bind parameters; i is an int we control.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// SaveTransactions inserts txs in one database transaction. Rows whose ID
// already exists are skipped, which makes re-importing a file a no-op.
func (s *Store) SaveTransactions(ctx context.Context, txs []domain.Transaction) (int, error) {
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer dbtx.Rollback() // no-op after a successful Commit
	stmt, err := dbtx.PrepareContext(ctx, `INSERT INTO transactions
		(id, time_unix_ns, exchange, kind, asset, qty, quote_asset, quote_amount, fee, fee_asset, external_id, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	now := s.now().UnixNano()
	inserted := 0
	for _, t := range txs {
		if t.ID == "" {
			return 0, fmt.Errorf("insert %s: %w: missing ID", t.Label(), domain.ErrInvalidTransaction)
		}
		res, err := stmt.ExecContext(ctx, t.ID, t.Time.UTC().UnixNano(), t.Exchange, string(t.Kind), t.Asset,
			t.Qty.RatString(), t.QuoteAsset, t.QuoteAmount.RatString(), t.Fee.RatString(), t.FeeAsset, t.ExternalID, now)
		if err != nil {
			return 0, fmt.Errorf("insert %s: %w", t.Label(), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		inserted += int(n)
	}
	if err := dbtx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// Transactions returns every transaction before until, oldest first.
func (s *Store) Transactions(ctx context.Context, until time.Time) ([]domain.Transaction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, time_unix_ns, exchange, kind, asset, qty, quote_asset,
		quote_amount, fee, fee_asset, external_id
		FROM transactions WHERE time_unix_ns < ? ORDER BY time_unix_ns, seq`, until.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("query transactions: %w", err)
	}
	defer rows.Close()
	var out []domain.Transaction
	for rows.Next() {
		var (
			t                     domain.Transaction
			ns                    int64
			kind                  string
			qty, quoteAmount, fee string
		)
		if err := rows.Scan(&t.ID, &ns, &t.Exchange, &kind, &t.Asset, &qty, &t.QuoteAsset, &quoteAmount, &fee, &t.FeeAsset, &t.ExternalID); err != nil {
			return nil, fmt.Errorf("scan transaction: %w", err)
		}
		t.Time = time.Unix(0, ns).UTC()
		t.Kind = domain.Kind(kind)
		var errs []error
		var e error
		t.Qty, e = domain.ParseRat(qty)
		errs = append(errs, e)
		t.QuoteAmount, e = domain.ParseRat(quoteAmount)
		errs = append(errs, e)
		t.Fee, e = domain.ParseRat(fee)
		errs = append(errs, e)
		if err := errors.Join(errs...); err != nil {
			return nil, fmt.Errorf("decode transaction %s: %w", t.ID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transactions: %w", err)
	}
	return out, nil
}
