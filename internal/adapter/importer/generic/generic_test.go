package generic

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

func TestParse(t *testing.T) {
	in := "\ufefftime,exchange,kind,asset,qty,quote_asset,quote_amount,fee,fee_asset,external_id\n" +
		"# comments are skipped\n" +
		"2026-01-05T14:30:00-03:00,Mercado,buy,btc,0.01,BRL,3500.50,1.75,brl,A-1\n" +
		"2026-01-06T10:00:00Z,mercado,transfer_out,BTC,0.005,,,0.0001,BTC,\n"
	txs, err := Importer{}.Parse(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions", len(txs))
	}
	b := txs[0]
	if !b.Time.Equal(time.Date(2026, 1, 5, 17, 30, 0, 0, time.UTC)) || b.Exchange != "mercado" || b.Kind != domain.KindBuy ||
		b.Asset != "BTC" || b.Qty.String() != "0.01" || b.QuoteAsset != "BRL" || b.QuoteAmount.String() != "3500.5" ||
		b.Fee.String() != "1.75" || b.FeeAsset != "BRL" || b.ExternalID != "A-1" {
		t.Errorf("buy = %+v", b)
	}
	if tr := txs[1]; tr.Kind != domain.KindTransferOut || !tr.QuoteAmount.IsZero() || tr.FeeAsset != "BTC" {
		t.Errorf("transfer = %+v", tr)
	}
}

func TestParseColumnOrderIsFree(t *testing.T) {
	in := "qty,asset,kind,exchange,time\n1,ETH,deposit,x,2026-01-01T00:00:00Z\n"
	txs, err := Importer{}.Parse(context.Background(), strings.NewReader(in))
	if err != nil || len(txs) != 1 || txs[0].Asset != "ETH" {
		t.Fatalf("txs=%+v err=%v", txs, err)
	}
}

func TestParseErrorsNameTheLine(t *testing.T) {
	tests := []struct {
		name, in, errSub string
	}{
		{"empty", "", "header row expected"},
		{"missing column", "time,exchange,kind,asset\n", `missing required column "qty"`},
		{"duplicate column", "time,time,exchange,kind,asset,qty\n", "duplicate column"},
		{"bad time", "time,exchange,kind,asset,qty\n2026-01-01 10:00,x,buy,BTC,1\n", "line 2: column time"},
		{"bad kind", "time,exchange,kind,asset,qty\n2026-01-01T10:00:00Z,x,trade,BTC,1\n", "line 2: column kind"},
		{"thousands separator", "time,exchange,kind,asset,qty\n2026-01-01T10:00:00Z,x,buy,BTC,\"1,000\"\n", "line 2: column qty"},
		{"ragged row", "time,exchange,kind,asset,qty\n2026-01-01T10:00:00Z,x\n", "wrong number of fields"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Importer{}.Parse(context.Background(), strings.NewReader(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error = %v, want containing %q", err, tt.errSub)
			}
		})
	}
}

func TestParseHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Importer{}.Parse(ctx, strings.NewReader("time,exchange,kind,asset,qty\n2026-01-01T10:00:00Z,x,buy,BTC,1\n"))
	if err == nil {
		t.Fatal("expected context error")
	}
}
