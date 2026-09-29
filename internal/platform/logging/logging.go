// Package logging builds the process logger: log/slog with a JSON handler,
// snake_case fields, written to stderr so stdout stays clean for reports.
package logging

import (
	"io"
	"log/slog"
)

// New returns a JSON logger at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).With("app", "costbasis")
}
