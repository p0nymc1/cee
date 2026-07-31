package main

import (
	"fmt"
	"os"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestPath = "examples/manifests/network-detection.json"

var egressIdentity = map[string]string{
	"203.0.113.10": "our HQ NAT egress — about 1,400 staff share this address",
	"203.0.113.11": "our VPN concentrator — about 900 remote workers egress here",
	"198.51.100.7": "shared CDN edge — also fronts the payment callback endpoint",
}

type asset struct {
	role       string
	critical   bool
	dependents int
}

var inventory = map[string]asset{
	"dc01":     {"domain controller", true, 1400},
	"jump01":   {"responder jump host", true, 60},
	"vpn-gw01": {"VPN gateway", true, 900},
	"coredb01": {"core database", true, 220},
	"ws-4471":  {"engineering workstation", false, 0},
	"ws-8802":  {"finance workstation", false, 0},
	"build-07": {"CI build agent", false, 3},
}

var breakGlass = map[string]bool{
	"svc-emergency": true,
	"ir-oncall":     true,
}

var containmentSOP = map[string]struct{ action, targetField string }{
	"network-detection.T1046_service_discovery": {"isolate_host", "target_host"},
	"network-detection.T1021_lateral_movement":  {"isolate_host", "target_host"},
	"network-detection.T1071_c2_beaconing":      {"block_address", "peer_ip"},
	"network-detection.T1048_exfiltration":      {"block_address", "peer_ip"},
	"network-detection.T1110_password_spray":    {"block_address", "peer_ip"},
}

func buildRuntime() (*intentrouter.Router, *execution.Engine, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)

	sb.RegisterProbe("netdet.assess_blast_radius", func(ctx map[string]any) (bool, string, error) {
		action, _ := ctx["response_action"].(string)
		target, _ := ctx["response_target"].(string)

		switch action {
		case "block_address":
			if who, ours := egressIdentity[target]; ours {
				return false, fmt.Sprintf("%s is %s — blocking it hits us, not the attacker", target, who), nil
			}
			return true, "", nil

		case "isolate_host":
			host, known := inventory[target]
			if !known {
				return false, fmt.Sprintf("host %q is not in inventory; isolating an unidentified asset is not safe to automate", target), nil
			}
			if host.critical {
				return false, fmt.Sprintf("%s is a %s — isolating it would cut off %d dependents", target, host.role, host.dependents), nil
			}
			return true, "", nil

		case "disable_account":
			if breakGlass[target] {
				return false, fmt.Sprintf("%q is a break-glass account — disabling it during an incident removes the way out", target), nil
			}
			return true, "", nil
		}
		return false, fmt.Sprintf("unknown response action %q", action), nil
	})

	hooks := manifest.Hooks{

		"netdet.select_response": func(ctx map[string]any) (map[string]any, error) {
			technique, _ := ctx["technique"].(string)
			sop, known := containmentSOP[technique]
			if !known {
				return nil, fmt.Errorf("no containment SOP for technique %q", technique)
			}
			target, _ := ctx[sop.targetField].(string)
			if target == "" {
				return nil, fmt.Errorf("alert carries no %s to act on", sop.targetField)
			}
			return map[string]any{"response_action": sop.action, "response_target": target}, nil
		},

		"netdet.apply_containment": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{
				"executed": fmt.Sprintf("%v on %v", ctx["response_action"], ctx["response_target"]),
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

type alert struct {
	summary    string
	confidence float64
	peerIP     string
	targetHost string
	note       string
}

func main() {
	router, engine, err := buildRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}

	for _, a := range feed() {
		handle(router, engine, a)
	}
}

func feed() []alert {
	return []alert{
		{
			summary: "horizontal port scan detected from internal host", confidence: 0.94,
			peerIP: "203.0.113.55", targetHost: "ws-4471",
			note: "ordinary workstation, nothing depends on it — safe to isolate",
		},
		{
			summary: "password spray against the vpn gateway", confidence: 0.97,
			peerIP: "203.0.113.11", targetHost: "vpn-gw01",
			note: "the 'attacker' address is our own VPN egress",
		},
		{
			summary: "periodic outbound connections with fixed interval", confidence: 0.88,
			peerIP: "198.51.100.7", targetHost: "build-07",
			note: "the beacon destination is a shared CDN node",
		},
		{
			summary: "lateral movement over rdp", confidence: 0.91,
			peerIP: "10.4.2.19", targetHost: "jump01",
			note: "the host to isolate is the responders' own way in",
		},
		{
			summary: "unusually large outbound transfer over dns", confidence: 0.96,
			peerIP: "203.0.113.90", targetHost: "ws-8802",
			note: "genuine exfiltration to an unrelated address",
		},
		{
			summary: "beaconing to an unclassified external host", confidence: 0.42,
			peerIP: "203.0.113.77", targetHost: "ws-4471",
			note: "technique matches, but the detector is unsure — no action on a maybe",
		},
		{
			summary: "printer firmware update failed twice", confidence: 0.99,
			peerIP: "10.1.1.5", targetHost: "ws-4471",
			note: "not an intrusion at all — no technique matches",
		},
	}
}

func handle(router *intentrouter.Router, engine *execution.Engine, a alert) {
	fmt.Printf("\nALERT  %s\n", a.summary)
	fmt.Printf("       peer=%s host=%s confidence=%.2f\n", a.peerIP, a.targetHost, a.confidence)
	fmt.Printf("       (%s)\n", a.note)

	match := router.Match("network-detection", a.summary)
	if !match.Matched {

		fmt.Printf("  -> no ATT&CK technique matched (best score %.2f); would fall through to extraction\n", match.Confidence)
		return
	}
	fmt.Printf("  -> matched %s (%.2f)\n", match.NodeRef, match.Confidence)

	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{
		"technique":           match.NodeRef,
		"detector_confidence": a.confidence,
		"peer_ip":             a.peerIP,
		"target_host":         a.targetHost,
	})
	if err != nil {
		fmt.Printf("  -> halted: %v\n", err)
		return
	}

	fmt.Printf("  -> %v\n", result.Output["disposition"])
	if executed, ok := result.Output["executed"]; ok {
		fmt.Printf("     action: %v\n", executed)
	}

	if why, ok := result.Output[execution.FailureReasonKey]; ok {
		fmt.Printf("     because: %v\n", why)
	}
	fmt.Printf("     trace: %v\n", result.Trace)
}
