package binance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

func parse(t *testing.T, in string) []domain.Transaction {
	t.Helper()
	txs, err := Importer{}.Parse(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	return txs
}

func TestParseCurrentLayout(t *testing.T) {
	in := "\ufeffDate(UTC),Pair,Side,Price,Executed,Amount,Fee\n" +
		"2024-06-01 12:00:00,BTCUSDT,BUY,60000,0.001BTC,60USDT,0.01BNB\n" +
		"2024-06-02 08:30:00,ETHBRL,SELL,20000,0.5ETH,\"10,000.5BRL\",10.0005BRL\n" +
		"2024-06-03 09:00:00,1INCHUSDT,BUY,0.5,100.51INCH,50.255USDT,0.11INCH\n" +
		"2024-06-04 09:00:00,SOLBTC,SELL,0.002,1SOL,0.002BTC,\n"
	txs := parse(t, in)
	if len(txs) != 4 {
		t.Fatalf("got %d rows", len(txs))
	}
	tests := []struct {
		kind                                     domain.Kind
		asset, qty, quote, amount, fee, feeAsset string
	}{
		{domain.KindBuy, "BTC", "0.001", "USDT", "60", "0.01", "BNB"},
		{domain.KindSell, "ETH", "0.5", "BRL", "10000.5", "10.0005", "BRL"},
		{domain.KindBuy, "1INCH", "100.5", "USDT", "50.255", "0.1", "1INCH"},
		{domain.KindSell, "SOL", "1", "BTC", "0.002", "0", ""},
	}
	for i, w := range tests {
		tx := txs[i]
		if tx.Kind != w.kind || tx.Asset != w.asset || tx.Qty.String() != w.qty || tx.QuoteAsset != w.quote ||
			tx.QuoteAmount.String() != w.amount || tx.Fee.String() != w.fee || tx.FeeAsset != w.feeAsset || tx.Exchange != "binance" {
			t.Errorf("row %d = %+v, want %+v", i, tx, w)
		}
		if err := tx.Validate(); err != nil {
			t.Errorf("row %d invalid: %v", i, err)
		}
	}
	if !txs[0].Time.Equal(time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("time = %s", txs[0].Time)
	}
}

func TestParseLegacyLayout(t *testing.T) {
	in := "Date(UTC),Market,Type,Price,Amount,Total,Fee,Fee Coin\n" +
		"2021-03-01 10:00:00,BNBUSDT,BUY,250,2,500,0.0015,BNB\n" +
		"2021-03-02 10:00:00,ETHBTC,SELL,0.03,1,0.03,0.00003,BTC\n"
	txs := parse(t, in)
	if len(txs) != 2 || txs[0].Asset != "BNB" || txs[0].QuoteAsset != "USDT" || txs[0].QuoteAmount.String() != "500" ||
		txs[1].Asset != "ETH" || txs[1].QuoteAsset != "BTC" || txs[1].FeeAsset != "BTC" || txs[1].Kind != domain.KindSell {
		t.Fatalf("txs = %+v", txs)
	}
}

func TestParseErrors(t *testing.T) {
	const h = "Date(UTC),Pair,Side,Price,Executed,Amount,Fee\n"
	tests := []struct{ name, in, errSub string }{
		{"empty", "", "header row expected"},
		{"unknown header", "Time,Coin,Amount\n", "unrecognised Binance header"},
		{"bad date", h + "01/06/2024,BTCUSDT,BUY,1,1BTC,1USDT,\n", "line 2: column Date(UTC)"},
		{"bad side", h + "2024-06-01 12:00:00,BTCUSDT,HOLD,1,1BTC,1USDT,\n", "neither BUY nor SELL"},
		{"pair mismatch", h + "2024-06-01 12:00:00,BTCUSDT,BUY,1,1ETH,1USDT,\n", "cannot match"},
		{"bad fee", h + "2024-06-01 12:00:00,BTCUSDT,BUY,1,1BTC,1USDT,BNB\n", "column Fee"},
		{"bad separator", h + "2024-06-01 12:00:00,BTCUSDT,BUY,1,\"1,5BTC\",1USDT,\n", "cannot match"},
		{"legacy unknown quote", "Date(UTC),Market,Type,Price,Amount,Total,Fee,Fee Coin\n2021-03-01 10:00:00,BNBXYZ,BUY,1,1,1,,\n", "cannot split market"},
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

func TestIdenticalRowsStayDistinct(t *testing.T) {
	row := "2024-06-01 12:00:00,BTCUSDT,BUY,60000,0.001BTC,60USDT,\n"
	txs := parse(t, "Date(UTC),Pair,Side,Price,Executed,Amount,Fee\n"+row+row)
	domain.AssignIDs(txs)
	if txs[0].ID == txs[1].ID {
		t.Fatal("two identical fills must get different IDs")
	}
}
