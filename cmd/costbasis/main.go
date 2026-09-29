// Command costbasis imports exchange exports, computes weighted-average cost
// basis in BRL and prints a monthly Brazilian tax report. Not tax advice.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	_ "time/tzdata" // embed the zone database: the container image has none
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
