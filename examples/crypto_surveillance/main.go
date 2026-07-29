// Command crypto_surveillance screens live crypto market data for anomalies
// worth a human's attention.
//
// It is market surveillance, not investment advice. Every rule is a fixed
// threshold written in a manifest or in deterministic Go; nothing here decides
// whether an asset is worth buying, and no model is asked to. The output is
// "this data point is unusual, someone should look" -- the same shape as the
// network detection domain, with market data in place of alerts.
//
// The interesting part is the guardrail. Acting on market data is dangerous
// for reasons that have nothing to do with the rules being wrong:
//
//   - a quote that is minutes stale describes a market that has moved on;
//   - a large percentage move on a thin book is noise, not a signal -- a few
//     hundred thousand dollars can move an illiquid asset several percent.
//
// Both are checked by a pre-execution sandbox probe, so a finding computed
// from data too stale or too thin to trust is held rather than raised. The
// probe reads and compares; it never writes, per handbook rule 1.2.
//
// Run it with `go run ./examples/crypto_surveillance`.
package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestPath = "examples/manifests/crypto-surveillance.json"

// Thresholds are constants rather than model judgements, so a finding can be
// argued with, reproduced, and changed in a reviewed commit.
const (
	materialMovePct = 2.0   // a 24h move at or beyond this is worth a look
	pegTolerancePct = 0.5   // a stablecoin this far from 1.00 is worth a look
	liquidityFloor  = 250e6 // below this 24h volume, a percentage move is noise
	maxQuoteAge     = 15 * time.Minute
)

// stablecoins are screened against their peg instead of against volatility:
// a 2% move in a coin that is supposed to be worth exactly one dollar means
// something entirely different from a 2% move in bitcoin.
var stablecoins = map[string]bool{"tether": true, "usd-coin": true, "dai": true}

var watchlist = []string{
	"bitcoin", "ethereum", "solana", "dogecoin",
	"tether", "usd-coin", // screened against the peg
	"chainlink", "litecoin", // thinner books, to exercise the liquidity guardrail
}

func buildRuntime() (*intentrouter.Router, *execution.Engine, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)

	// The guardrail: is this data good enough to raise a finding on?
	sb.RegisterProbe("crypto.assess_signal_quality", func(ctx map[string]any) (bool, string, error) {
		age, _ := ctx["quote_age_seconds"].(float64)
		volume, _ := ctx["volume_24h"].(float64)

		if age > maxQuoteAge.Seconds() {
			return false, fmt.Sprintf("quote is %.0f minutes old; the market has moved on", age/60), nil
		}
		if volume < liquidityFloor {
			return false, fmt.Sprintf(
				"24h volume is $%.0fM, below the $%.0fM floor — a percentage move on a book this thin is noise",
				volume/1e6, liquidityFloor/1e6), nil
		}
		return true, "", nil
	})

	hooks := manifest.Hooks{
		// Derived facts only. Nothing here decides anything.
		"crypto.enrich": func(ctx map[string]any) (map[string]any, error) {
			price, _ := ctx["price_usd"].(float64)
			change, _ := ctx["change_pct_24h"].(float64)
			return map[string]any{
				"abs_change_pct":    math.Abs(change),
				"peg_deviation_pct": math.Abs(price-1.0) * 100,
				"direction":         direction(change),
			}, nil
		},

		// Applies the one rule that fits the asset class. Deterministic: the
		// same quote always produces the same finding.
		"crypto.assess": func(ctx map[string]any) (map[string]any, error) {
			asset, _ := ctx["asset"].(string)
			if stablecoins[asset] {
				dev, _ := ctx["peg_deviation_pct"].(float64)
				if dev >= pegTolerancePct {
					return map[string]any{
						"finding": "peg_deviation",
						"detail":  fmt.Sprintf("%.2f%% away from its 1.00 peg", dev),
					}, nil
				}
				return map[string]any{"finding": "none", "detail": fmt.Sprintf("holding peg (%.2f%% off)", dev)}, nil
			}

			abs, _ := ctx["abs_change_pct"].(float64)
			if abs >= materialMovePct {
				dir, _ := ctx["direction"].(string)
				return map[string]any{
					"finding": "material_move",
					"detail":  fmt.Sprintf("%.2f%% %s over 24h", abs, dir),
				}, nil
			}
			return map[string]any{"finding": "none", "detail": fmt.Sprintf("%.2f%% over 24h", abs)}, nil
		},

		// Only reached once the probe judged the data trustworthy.
		"crypto.raise_alert": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{
				"raised": fmt.Sprintf("%v: %v", ctx["finding"], ctx["detail"]),
			}, nil
		},
	}

	domain, err := manifest.Load(data, hooks, stdlib.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("loading manifest: %w", err)
	}
	registry.NewRegistry(router, engine).RegisterDomain(*domain)
	return router, engine, nil
}

func direction(change float64) string {
	if change < 0 {
		return "down"
	}
	return "up"
}

// screen runs one quote through the engine and returns a one-line verdict.
func screen(engine *execution.Engine, entry string, q Quote, now time.Time) (string, error) {
	result, err := engine.Run(entry, map[string]any{
		"asset":             q.Asset,
		"price_usd":         q.USD,
		"change_pct_24h":    q.ChangePct24,
		"volume_24h":        q.Volume24,
		"quote_age_seconds": now.Sub(q.QuotedAt).Seconds(),
	})
	if err != nil {
		return "", err
	}

	line := fmt.Sprintf("%-10s $%-11s %v", q.Asset, trimPrice(q.USD), result.Output["disposition"])
	if raised, ok := result.Output["raised"]; ok {
		line += fmt.Sprintf("  [%v]", raised)
	} else if detail, ok := result.Output["detail"]; ok {
		line += fmt.Sprintf("  (%v)", detail)
	}
	// The guardrail held it: say what the probe objected to, or the hold is
	// just as opaque as no answer at all.
	if result.Output["disposition"] == heldDisposition {
		if why, ok := result.Output[execution.FailureReasonKey].(string); ok {
			line += "\n" + strings.Repeat(" ", 23) + "because: " + why
		}
	}
	return line, nil
}

// heldDisposition must match the manifest's hold_low_quality step.
const heldDisposition = "not flagged: the data is not good enough to act on"

func trimPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.0f", p)
	case p >= 1:
		return fmt.Sprintf("%.2f", p)
	default:
		return fmt.Sprintf("%.4f", p)
	}
}

func main() {
	router, engine, err := buildRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}

	match := router.Match("crypto-surveillance", "surveillance sweep over quoted prices")
	if !match.Matched {
		fmt.Fprintln(os.Stderr, "no surveillance intent matched")
		os.Exit(1)
	}

	fmt.Println("== 虚拟货币异常监控 / crypto market surveillance ==")
	fmt.Println("Fixed thresholds over live quotes. Anomaly flagging, not investment advice.")
	fmt.Printf("Rules: move >= %.1f%% · stablecoin peg +/- %.1f%% · liquidity floor $%.0fM · quotes younger than %s\n\n",
		materialMovePct, pegTolerancePct, liquidityFloor/1e6, maxQuoteAge)

	quotes, err := NewFeed().Quotes(watchlist)
	if err != nil {
		// Reported, never faked, and never fatal: a surveillance sweep that
		// could not read the market is a fact worth printing, not a crash.
		fmt.Printf("market data unavailable: %v\n", err)
		fmt.Println("no sweep performed — the engine is only as current as its feed")
		return
	}

	now := time.Now().UTC()
	fmt.Printf("swept %d assets at %s\n\n", len(quotes), now.Format("2006-01-02 15:04 UTC"))
	for _, q := range quotes {
		line, err := screen(engine, match.EntryWorkflowRef, q, now)
		if err != nil {
			fmt.Printf("%-10s halted: %v\n", q.Asset, err)
			continue
		}
		fmt.Println(line)
	}
}
