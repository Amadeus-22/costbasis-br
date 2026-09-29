# 0002. Tax rules are dated data, and the shipped rules are unverified

- Status: accepted
- Date: 2026-09-29

## Context

Brazilian crypto tax rules change often. Thresholds, rates, reporting
obligations and even which regime applies have all changed. In 2025 alone a
provisional measure proposed replacing the whole regime, and then it lapsed.
A tool that hard-codes "15% above R$35,000" is wrong the day a rule changes,
and it cannot recompute an earlier year under the rules that applied then.

The author is not a tax professional. Most primary legal texts could not be
loaded during research (planalto.gov.br and normas.receita.fazenda.gov.br
timed out), so several rules were confirmed only through secondary sources.

## Decision

1. Rules live in YAML rulesets (`rulesets/*.yaml`), loaded by
   `internal/adapter/rulesets` and evaluated by the pure `internal/domain/tax`.
   The engine contains no thresholds, rates or dates.
2. Every rule has `effective_from` and optionally `effective_to`, plus a
   `source` and a `verified` flag. For each month or date, the evaluation
   picks the rule in force. Overlapping ranges within a rule family are
   rejected. Unknown YAML keys are rejected too, so a typo cannot drop a rule.
3. Amounts and rates are parsed from the literal YAML text into exact
   rationals, never through `float64`.
4. The shipped ruleset `br-example` has `verified: false` on the ruleset and on
   every rule. Reports print "NOT TAX ADVICE", and they flag each obligation
   that comes from an unverified rule.

## What the research found (September 2026)

Confidence: **high** = several consistent sources, including official ones.
**Medium** = consistent secondary sources, or official text seen only in part.
**Low** = a single or indirect source.

| Rule | Value in `br-example` | Source | Confidence |
|---|---|---|---|
| Cost method | weighted average per asset | Brazilian practice and GCAP (e.g. [blueconsult](https://blueconsult.com.br/custo-medio-ponderado-cripto/)). No Receita text found that states it for crypto | medium |
| Swaps are taxable | crypto-to-crypto trade = disposal | Solução de Consulta Cosit 214/2021 (via secondary sources) | medium |
| Monthly exemption | total monthly sales ≤ R$35,000 → exempt | Lei 9.250/1995 art. 22, II, with the wording of Lei 11.196/2005 (text not reloaded) | medium-high |
| Exemption scope | all crypto sold in the month, of any type | [Receita Federal, 2023-03](https://www.gov.br/receitafederal/pt-br/assuntos/noticias/2023/marco/receita-federal-esclarece-sobre-declaracao-de-operacoes-com-criptoativos) (search snippet; page did not render) | high |
| Rates | 15% up to R$5M; 17.5% to R$10M; 20% to R$30M; 22.5% above, marginal | Lei 8.981/1995 art. 21, amended by Lei 13.259/2016 | high (text not reloaded) |
| Payment | DARF 4600, last business day of the following month | [Mercado Bitcoin IRPF guide](https://www.mercadobitcoin.com.br/irpf), [blueconsult 2026](https://blueconsult.com.br/imposto-criptomoedas-brasil-2026/) | high |
| Loss offset (domestic) | none between disposals | No provision found; conservative reading | **low** |
| Assets on foreign exchanges | since 2024-01-01: 15% on the year's net gain, annual, no R$35k exemption | Lei 14.754/2023; Receita's reading in Solução de Consulta Cosit 130/2026 (seen only in [news](https://bitnoticias.com.br/noticias/receita-federal-imposto-criptoativos/)); [Binance FAQ](https://www.binance.com/pt-BR/support/faq/detail/9095ba97b526440bb0173776c26f73b1); [cmtadv](https://cmtadv.com.br/pt/operacoes-com-criptoativos-no-exterior-e-as-novas-regras-da-lei-no-14-754-2023/). Some practitioners dispute this reading | medium |
| Loss offset (foreign) | within the year; carry-forward not modelled | Our reading of Lei 14.754/2023, not verified | **low** |
| Monthly report (until 2026-06) | IN RFB 1.888/2019: operations abroad or without a Brazilian exchange > R$30,000 in a month | IN RFB 1.888/2019 art. 6 (widely cited; text not loaded) | high |
| Monthly report (from 2026-07) | DeCripto, IN RFB 2.291/2025: > R$35,000 in a month; due last business day of the following month | [IN 2.291/2025 text](https://www.normaslegais.com.br/legislacao/instrucao-normativa-rfb-2291-2025.htm) arts. 5 §3, 12, 18, 19; [Receita DeCripto page](https://www.gov.br/receitafederal/pt-br/assuntos/orientacao-tributaria/declaracoes-e-demonstrativos/criptoativos/decripto) | high for dates and threshold; **low** for our assumption that only operations outside Brazilian providers count |
| Annual return | declare each crypto with acquisition cost ≥ R$5,000 (Bens e Direitos group 08: 01 BTC, 02 altcoins, 03 stablecoins, 10 NFT, 99 others) | [Receita 2023](https://www.gov.br/receitafederal/pt-br/assuntos/noticias/2023/marco/receita-federal-esclarece-sobre-declaracao-de-operacoes-com-criptoativos), [Ministério da Fazenda 2023](https://www.gov.br/fazenda/pt-br/assuntos/noticias/2023/abril/receita-mostra-como-informar-renda-variavel-e-criptoativos-na-declaracao-do-imposto-de-renda) | high (start date in the ruleset is a guess) |

### Changes in 2025–2026 that were checked

- **MP 1.303/2025** proposed a flat 17.5% rate on crypto gains, the end of the
  R$35,000 exemption and quarterly computation from 2026. It lapsed on
  2025-10-08 without a vote, so the previous rules still apply
  ([InfoMoney](https://www.infomoney.com.br/minhas-financas/mp-1-303-cai-no-congresso-veja-como-fica-a-tributacao-dos-investimentos-agora/),
  [Exame](https://exame.com/future-of-money/mp-1-303-com-rejeicao-na-camara-tributacao-de-criptos-nao-vai-subir-em-2026-veja-regras-atuais/)).
  Confidence: high. The ruleset does not model it.
- **Lei 15.270/2025** (dividend taxation and a minimum tax on high incomes from
  2026) reportedly leaves most capital gains out of the minimum-tax base. It
  does not change how crypto gains are computed
  ([Mayer Brown](https://www.mayerbrown.com/pt/insights/publications/2025/12/enactment-of-law-no-15270-2025-which-establishes-dividend-taxation-expands-the-exemption-threshold-and-introduces-a-minimum-tax-on-high-incomes)).
  Confidence: medium. Not modelled.
- **IOF on crypto purchases and transfers abroad** (3.5%) was put to public
  consultation in February 2026. We found no evidence that it was enacted.
  Confidence: low. Not modelled.
- **Custody classification.** Whether a given exchange counts as "abroad" is a
  fact about the account, not a legal rule. `foreign_exchanges` lists only
  `binance`, based on Binance's own FAQ, and users are told to edit it.

## Consequences

- A rule change is a reviewed data change: add a new dated entry and close the
  old one with `effective_to`. Past years keep their rules.
- A month that no rule covers is an error, never a silent default.
- The `verified` flags stay `false` until someone qualified checks each source.
  Flipping them is a deliberate act with its own commit.
- Income from staking and airdrops is valued as an acquisition cost. Whether
  it is also taxable income when received is not modelled.
