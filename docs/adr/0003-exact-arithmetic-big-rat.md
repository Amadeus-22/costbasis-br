# 0003. Exact arithmetic with math/big.Rat, rounding only at the output edge

- Status: accepted
- Date: 2026-09-29

## Context

Crypto quantities have up to 18 decimal places (8 for BTC). Prices run from
fractions of a centavo to hundreds of thousands of reais. A weighted average
divides by the quantity held, so it repeats: 10,000 BRL for 3 ETH is
3,333.33… per ETH. `float64` cannot represent 0.1. Scaled integers need a
fixed scale that fits both satoshis and BRL, and they still have to round
every average, which leaves a residue when a position is sold down to zero.

## Decision

- Every amount and quantity is a `domain.Decimal`, a small immutable wrapper
  around `*big.Rat`. Each operation returns a new value, which removes the
  classic `big.Rat` aliasing bug (`z.Add(z, x)` mutating a value shared
  elsewhere).
- Parsing accepts only plain decimals: no exponents, no thousands separators,
  no `NaN`. The Binance importer's strict `1,234.56` form is the one explicit
  exception, and it lives in the adapter.
- Storage keeps the exact value as text (`RatString`, e.g. `1/3`), so a
  database round trip is lossless.
- Nothing is rounded in `domain`, `app` or the tax evaluation. The
  `render` adapter is the only place that rounds: BRL to 2 places, quantities
  to 8, half away from zero. The CLI and the HTTP API share it, so they print
  identical numbers.

## Consequences

- "Buy then sell everything leaves zero quantity and zero cost" holds exactly.
  So does "the cost of all disposals plus the remaining cost equals everything
  paid". Property tests check both over hundreds of random histories.
- The rounded monthly figures may not add up to the rounded yearly total by a
  centavo, because each figure is rounded from the exact total, not summed
  from rounded parts. That is the right way round for a tax estimate.
- `big.Rat` is slower than integers, and denominators grow along long
  histories. For a personal portfolio (thousands of transactions) this is
  measured in milliseconds and is not a concern. If it ever is, the fix is to
  normalise positions to a very fine fixed scale, not to use floats.
