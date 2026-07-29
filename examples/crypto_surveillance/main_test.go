package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p0nymc1/cee/execution"
)

func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// Live quotes are what the demo screens, but a test that depends on today's
// market proves nothing and fails for reasons unrelated to the code. Every
// case below is a fixed quote.
func verdict(t *testing.T, q Quote, age time.Duration) map[string]any {
	t.Helper()
	router, engine, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	match := router.Match("crypto-surveillance", "surveillance sweep over quoted prices")
	if !match.Matched {
		t.Fatal("the surveillance intent should match")
	}
	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{
		"asset":             q.Asset,
		"price_usd":         q.USD,
		"change_pct_24h":    q.ChangePct24,
		"volume_24h":        q.Volume24,
		"quote_age_seconds": age.Seconds(),
	})
	if err != nil {
		t.Fatalf("workflow halted: %v", err)
	}
	return result.Output
}

func TestMaterialMoveOnALiquidBookIsFlagged(t *testing.T) {
	out := verdict(t, Quote{Asset: "bitcoin", USD: 64000, ChangePct24: -7.4, Volume24: 30e9}, time.Minute)
	if out["disposition"] != "flagged for review" {
		t.Fatalf("expected a flag, got %v", out["disposition"])
	}
	raised, _ := out["raised"].(string)
	if !strings.Contains(raised, "material_move") || !strings.Contains(raised, "down") {
		t.Fatalf("the finding should name the move and its direction, got %q", raised)
	}
}

func TestQuietMarketIsNotFlagged(t *testing.T) {
	out := verdict(t, Quote{Asset: "bitcoin", USD: 64000, ChangePct24: 0.4, Volume24: 30e9}, time.Minute)
	if out["disposition"] != "nothing material" {
		t.Fatalf("a 0.4%% move should be quiet, got %v", out["disposition"])
	}
	if _, raised := out["raised"]; raised {
		t.Fatal("a quiet market must not raise anything")
	}
}

// The guardrail that matters: the rule fired correctly and the data still is
// not worth acting on.
func TestMaterialMoveOnAThinBookIsHeld(t *testing.T) {
	out := verdict(t, Quote{Asset: "chainlink", USD: 8.35, ChangePct24: -9.1, Volume24: 40e6}, time.Minute)
	if out["disposition"] != heldDisposition {
		t.Fatalf("a thin book should be held, got %v", out["disposition"])
	}
	if _, raised := out["raised"]; raised {
		t.Fatal("nothing should be raised on an illiquid book")
	}
	why, _ := out[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "noise") {
		t.Fatalf("the hold should explain itself, got %q", why)
	}
}

func TestStaleQuoteIsHeld(t *testing.T) {
	out := verdict(t, Quote{Asset: "bitcoin", USD: 64000, ChangePct24: -7.4, Volume24: 30e9}, 2*time.Hour)
	if out["disposition"] != heldDisposition {
		t.Fatalf("a two-hour-old quote should be held, got %v", out["disposition"])
	}
	why, _ := out[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "moved on") {
		t.Fatalf("the hold should say the quote is stale, got %q", why)
	}
}

// A stablecoin is screened against its peg, not against volatility: the same
// percentage means something different for an asset that should be worth
// exactly one dollar.
func TestStablecoinDepegIsFlagged(t *testing.T) {
	out := verdict(t, Quote{Asset: "tether", USD: 0.972, ChangePct24: -2.8, Volume24: 40e9}, time.Minute)
	if out["disposition"] != "flagged for review" {
		t.Fatalf("a 2.8%% depeg should be flagged, got %v", out["disposition"])
	}
	raised, _ := out["raised"].(string)
	if !strings.Contains(raised, "peg_deviation") {
		t.Fatalf("expected a peg finding rather than a volatility one, got %q", raised)
	}
}

func TestStablecoinHoldingItsPegIsQuiet(t *testing.T) {
	out := verdict(t, Quote{Asset: "usd-coin", USD: 0.9998, ChangePct24: 0.01, Volume24: 12e9}, time.Minute)
	if out["disposition"] != "nothing material" {
		t.Fatalf("a coin on its peg should be quiet, got %v", out["disposition"])
	}
}

// A stablecoin that barely moves in percentage terms can still be badly off
// peg; screening it as volatility would miss exactly the case that matters.
func TestDepegIsCaughtEvenWhenTheDailyMoveIsSmall(t *testing.T) {
	out := verdict(t, Quote{Asset: "tether", USD: 0.988, ChangePct24: -1.1, Volume24: 40e9}, time.Minute)
	if out["disposition"] != "flagged for review" {
		t.Fatalf("1.2%% off peg should be flagged even on a 1.1%% daily move, got %v", out["disposition"])
	}
}

type stubDoer struct {
	body   string
	status int
	err    error
}

func (s stubDoer) Do(*http.Request) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	code := s.status
	if code == 0 {
		code = http.StatusOK
	}
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestFeedParsesQuotesInRequestedOrder(t *testing.T) {
	feed := &Feed{Endpoint: "http://example.invalid", Client: stubDoer{body: `{
		"ethereum": {"usd": 1900.5, "usd_24h_change": -3.2, "usd_24h_vol": 10000000000, "last_updated_at": 1700000000},
		"bitcoin":  {"usd": 64000.0, "usd_24h_change": 1.5, "usd_24h_vol": 25000000000, "last_updated_at": 1700000060}
	}`}}

	quotes, err := feed.Quotes([]string{"bitcoin", "ethereum"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Order follows the request, not the JSON object, so a sweep reads the
	// same way every run.
	if len(quotes) != 2 || quotes[0].Asset != "bitcoin" || quotes[1].Asset != "ethereum" {
		t.Fatalf("expected bitcoin then ethereum, got %+v", quotes)
	}
	if quotes[0].USD != 64000 || quotes[1].ChangePct24 != -3.2 {
		t.Fatalf("quote fields did not survive decoding: %+v", quotes)
	}
	if quotes[0].QuotedAt.IsZero() {
		t.Fatal("the quote timestamp is what the staleness guardrail runs on")
	}
}

func TestFeedReportsAnUnreachableProvider(t *testing.T) {
	feed := &Feed{Endpoint: "http://example.invalid", Client: stubDoer{status: http.StatusTooManyRequests, body: "{}"}}
	if _, err := feed.Quotes([]string{"bitcoin"}); err == nil {
		t.Fatal("a rate-limited provider must be reported, not silently treated as an empty market")
	}
}

func TestFeedSkipsAssetsTheProviderDidNotReturn(t *testing.T) {
	feed := &Feed{Endpoint: "http://example.invalid", Client: stubDoer{body: `{"bitcoin": {"usd": 64000, "last_updated_at": 1700000000}}`}}
	quotes, err := feed.Quotes([]string{"bitcoin", "not-a-real-coin"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Missing is missing. Inventing a zero-priced asset would hand the rules a
	// fabricated 100% depeg.
	if len(quotes) != 1 || quotes[0].Asset != "bitcoin" {
		t.Fatalf("expected only the asset that was returned, got %+v", quotes)
	}
}
