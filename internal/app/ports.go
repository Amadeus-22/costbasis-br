// Package app holds the use cases (import, recompute, report) and the ports
// they need. Adapters implement the ports; cmd wires them together.
package app

import (
	"context"
	"io"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

// Store persists normalised transactions.
type Store interface {
	// SaveTransactions stores txs atomically, skipping any whose ID already
	// exists, and returns how many were new.
	SaveTransactions(ctx context.Context, txs []domain.Transaction) (inserted int, err error)
	// Transactions returns every transaction strictly before until, ordered
	// by time and then by insertion order.
	Transactions(ctx context.Context, until time.Time) ([]domain.Transaction, error)
	// Ping checks the store is reachable.
	Ping(ctx context.Context) error
}

// RateSource prices assets in BRL.
type RateSource interface {
	// Rate returns the BRL price of one unit of asset on the given calendar
	// date. A missing rate is an error wrapping domain.ErrRateNotFound that
	// names the date and pair; implementations never interpolate.
	Rate(ctx context.Context, asset string, date time.Time) (domain.Decimal, error)
}

// Importer parses one exchange's export into normalised transactions.
type Importer interface {
	Parse(ctx context.Context, r io.Reader) ([]domain.Transaction, error)
}

// RulesetLoader loads and validates a ruleset by ID.
type RulesetLoader interface {
	Load(ctx context.Context, id string) (tax.Ruleset, error)
}
