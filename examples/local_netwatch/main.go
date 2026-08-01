package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestPath = "examples/manifests/local-netwatch.json"

type Policy struct {
	RiskyPeers map[string]string `json:"risky_peers"`

	ExpectedTalkers []string `json:"expected_talkers"`

	PlaintextPorts map[string]string `json:"plaintext_ports"`
}

func DefaultPolicy() Policy {
	return Policy{
		RiskyPeers: map[string]string{},
		ExpectedTalkers: []string{
			"Google Chrome", "firefox", "Safari", "com.apple.WebKit.Networking",
			"Slack", "zoom.us", "Spotify", "Dropbox", "node", "Code Helper",
		},
		PlaintextPorts: map[string]string{
			"21":   "FTP — credentials and data in the clear",
			"23":   "Telnet — credentials in the clear",
			"25":   "SMTP without TLS",
			"110":  "POP3 — credentials in the clear",
			"143":  "IMAP without TLS",
			"389":  "LDAP without TLS",
			"3389": "RDP exposed across the internet",
		},
	}
}

func LoadPolicy(path string) (Policy, error) {
	policy := DefaultPolicy()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	var fromFile Policy
	if err := json.Unmarshal(data, &fromFile); err != nil {
		return policy, fmt.Errorf("%s: %w", path, err)
	}
	if fromFile.RiskyPeers != nil {
		policy.RiskyPeers = fromFile.RiskyPeers
	}
	if fromFile.ExpectedTalkers != nil {
		policy.ExpectedTalkers = fromFile.ExpectedTalkers
	}
	if fromFile.PlaintextPorts != nil {
		policy.PlaintextPorts = fromFile.PlaintextPorts
	}
	return policy, nil
}

func buildRuntime(policy Policy) (*intentrouter.Router, *execution.Engine, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	expected := map[string]bool{}
	for _, name := range policy.ExpectedTalkers {
		expected[name] = true
	}

	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)

	sb.RegisterProbe("netwatch.assess_alert_worth", func(ctx map[string]any) (bool, string, error) {
		finding, _ := ctx["finding"].(string)
		process, _ := ctx["process"].(string)

		if finding == "risky_peer" {
			return true, "", nil
		}
		if expected[process] {
			return false, fmt.Sprintf(
				"%q is on your expected-talkers list, and this finding is %q rather than a risky peer",
				process, finding), nil
		}
		return true, "", nil
	})

	hooks := manifest.Hooks{

		"netwatch.enrich": func(ctx map[string]any) (map[string]any, error) {
			peer, _ := ctx["peer_ip"].(string)
			return map[string]any{"peer_is_public": isPublic(peer)}, nil
		},

		"netwatch.assess": func(ctx map[string]any) (map[string]any, error) {
			peer, _ := ctx["peer_ip"].(string)
			port, _ := ctx["peer_port"].(float64)
			portKey := fmt.Sprintf("%.0f", port)

			if why, listed := policy.RiskyPeers[peer]; listed {
				return map[string]any{
					"finding": "risky_peer",
					"detail":  fmt.Sprintf("%s is on your risky list: %s", peer, why),
				}, nil
			}
			if why, plaintext := policy.PlaintextPorts[portKey]; plaintext {
				return map[string]any{
					"finding": "plaintext_protocol",
					"detail":  fmt.Sprintf("port %s to the internet — %s", portKey, why),
				}, nil
			}
			return map[string]any{"finding": "none", "detail": ""}, nil
		},

		"netwatch.raise": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"alert": ctx["detail"]}, nil
		},
	}

	domain, err := manifest.Load(data, hooks, stdlib.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("loading manifest: %w", err)
	}
	registry.NewRegistry(router, engine).RegisterDomain(*domain)
	return router, engine, nil
}

func screen(engine *execution.Engine, entry string, c Conn) (string, error) {
	result, runErr := engine.Run(entry, map[string]any{
		"process":   c.Process,
		"pid":       float64(c.PID),
		"peer_ip":   c.PeerIP,
		"peer_port": float64(c.PeerPort),
	})
	if runErr != nil {
		return "", runErr
	}

	line := fmt.Sprintf("%-11v %s", result.Output["disposition"], c)
	if alert, ok := result.Output["alert"]; ok {
		line += fmt.Sprintf("\n            %v", alert)
	}
	if why, ok := result.Output[execution.FailureReasonKey].(string); ok &&
		result.Output["disposition"] == "not worth alerting" {
		line += fmt.Sprintf("\n            (%s)", why)
	}
	return line, nil
}

func main() {
	policy, err := LoadPolicy("netwatch.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "policy:", err)
		os.Exit(1)
	}
	router, engine, err := buildRuntime(policy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}
	match := router.Match("local-netwatch", "review established connections")
	if !match.Matched {
		fmt.Fprintln(os.Stderr, "no screening intent matched")
		os.Exit(1)
	}

	conns, err := Established()
	if err != nil {

		fmt.Fprintln(os.Stderr, "could not read connections:", err)
		os.Exit(1)
	}

	fmt.Println("== local outbound connection screening ==")
	fmt.Printf("%d established connections · %d risky peers listed · %d plaintext ports\n",
		len(conns), len(policy.RiskyPeers), len(policy.PlaintextPorts))
	fmt.Println("Rules are in netwatch.json — this ships no threat intelligence.")
	fmt.Println()

	counts := map[string]int{}
	lines := make([]string, 0, len(conns))
	for _, c := range conns {
		line, err := screen(engine, match.EntryWorkflowRef, c)
		if err != nil {
			fmt.Printf("%-11s %s: %v\n", "halted", c, err)
			continue
		}
		lines = append(lines, line)
		counts[firstWord(line)]++
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}

	fmt.Printf("\nFLAGGED %d · suppressed %d · ok %d · internal %d\n",
		counts["FLAGGED"], counts["not"], counts["ok"], counts["internal"])
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}
