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

const (
	materialMovePct = 2.0
	pegTolerancePct = 0.5
	liquidityFloor  = 250e6
	maxQuoteAge     = 15 * time.Minute
)

var stablecoins = map[string]bool{"tether": true, "usd-coin": true, "dai": true}

var watchlist = []string{
	"bitcoin", "ethereum", "solana", "dogecoin",
	"tether", "usd-coin",
	"chainlink", "litecoin",
}

func buildRuntime() (*intentrouter.Router, *execution.Engine, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)

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

		"crypto.enrich": func(ctx map[string]any) (map[string]any, error) {
			price, _ := ctx["price_usd"].(float64)
			change, _ := ctx["change_pct_24h"].(float64)
			return map[string]any{
				"abs_change_pct":    math.Abs(change),
				"peg_deviation_pct": math.Abs(price-1.0) * 100,
				"direction":         direction(change),
			}, nil
		},

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

	if result.Output["disposition"] == heldDisposition {
		if why, ok := result.Output[execution.FailureReasonKey].(string); ok {
			line += "\n" + strings.Repeat(" ", 23) + "because: " + why
		}
	}
	return line, nil
}

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

	fmt.Println("== crypto market surveillance ==")
	fmt.Println("Fixed thresholds over live quotes. Anomaly flagging, not investment advice.")
	fmt.Printf("Rules: move >= %.1f%% · stablecoin peg +/- %.1f%% · liquidity floor $%.0fM · quotes younger than %s\n\n",
		materialMovePct, pegTolerancePct, liquidityFloor/1e6, maxQuoteAge)

	quotes, err := NewFeed().Quotes(watchlist)
	if err != nil {

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
