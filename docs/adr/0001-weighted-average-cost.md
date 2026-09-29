# 0001. Weighted average cost, not FIFO

- Status: accepted
- Date: 2026-09-29

## Context

A capital gain is proceeds minus the cost of what was sold. When the same coin
was bought several times at different prices, "the cost of what was sold"
needs a method. Global tools default to FIFO (first in, first out), sometimes
with specific identification, because that is what the US and UK expect.

Brazilian practice for individuals is the **weighted average acquisition cost**
(*custo médio ponderado*) per asset: each purchase is added to a running total
cost, and a sale carries away `quantity sold × total cost ÷ quantity held`.
The Receita Federal's capital gains program (GCAP) and every Brazilian guide we
found work this way. We did not find a Receita text that states the method
for crypto in so many words (see ADR 0002), so this is recorded as the
prevailing practice rather than a quoted rule.

## Decision

The engine (`internal/domain/costbasis`) keeps one position per asset, holding
a quantity and a total BRL cost, across all exchanges and wallets of the
taxpayer. It never keeps lots.

| Transaction | Effect |
|---|---|
| buy | acquire `qty` at `quote value + fee value`; a crypto quote (e.g. USDT) is **disposed of** at the quote value, because a swap is a sale |
| sell | dispose of `qty` for `quote value − fee value`; a crypto quote is acquired at the quote value |
| trading fee paid in crypto | also a disposal of the fee asset at its market value |
| income (staking, airdrop) | acquire at market value |
| fee (stand-alone) | disposal at market value |
| deposit, withdrawal, transfer_in, transfer_out | own-wallet movement, no taxable event; a network fee paid in the coin removes quantity but keeps the cost |

A disposal larger than the position is an error that names the transaction.
It always means the imported history is incomplete, and guessing a cost of
zero (the usual fallback in other tools) would overstate the tax.

## Consequences

- Selling everything removes exactly the total cost. Rational arithmetic
  (ADR 0003) means no residue is left; a property test checks this.
- Order matters. Transactions are replayed by time. Equal timestamps keep
  import order, which the store preserves.
- There is one pool per asset, even though Receita reads Lei 14.754/2023 as
  taxing assets held abroad separately (ADR 0002). Splitting pools by custody
  location is a possible refinement. We did not adopt it, because we found no
  guidance on how costs move between pools when coins are transferred.
- Transfer network fees are kept in the cost basis. The alternative is to
  treat them as a disposal at market value. That also has support in the
  literature, and it would make each transfer a taxable event. We chose
  conservation of cost because it is simpler to audit.
- FIFO could be added later as a second engine behind the same `Entry` input.
  Nothing outside the engine depends on the method.
