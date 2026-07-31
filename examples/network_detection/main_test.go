package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/execution"
)

func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func runAlert(t *testing.T, a alert) (map[string]any, []string, bool) {
	t.Helper()
	router, engine, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	match := router.Match("network-detection", a.summary)
	if !match.Matched {
		return nil, nil, false
	}
	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{
		"technique":           match.NodeRef,
		"detector_confidence": a.confidence,
		"peer_ip":             a.peerIP,
		"target_host":         a.targetHost,
	})
	if err != nil {
		t.Fatalf("workflow halted: %v", err)
	}
	return result.Output, result.Trace, true
}

func TestOrdinaryHostIsContainedAutomatically(t *testing.T) {
	out, trace, matched := runAlert(t, alert{
		summary: "horizontal port scan detected from internal host", confidence: 0.94,
		peerIP: "203.0.113.55", targetHost: "ws-4471",
	})
	if !matched {
		t.Fatal("expected the scan to match a technique")
	}
	if out["executed"] != "isolate_host on ws-4471" {
		t.Fatalf("expected the host to be isolated, got %v", out["executed"])
	}
	if trace[len(trace)-1] != "record_contained" {
		t.Fatalf("expected the contained path, got %v", trace)
	}
}

func TestBlockingOurOwnEgressIsRefused(t *testing.T) {
	out, trace, matched := runAlert(t, alert{
		summary: "password spray against the vpn gateway", confidence: 0.97,
		peerIP: "203.0.113.11", targetHost: "vpn-gw01",
	})
	if !matched {
		t.Fatal("expected the spray to match a technique")
	}
	if _, executed := out["executed"]; executed {
		t.Fatalf("containment must not have run, got %v", out["executed"])
	}
	if trace[len(trace)-1] != "hold_for_analyst" {
		t.Fatalf("expected the analyst path, got %v", trace)
	}

	why, _ := out[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "900 remote workers") {
		t.Fatalf("the refusal must explain the blast radius, got %q", why)
	}
}

func TestBlockingSharedInfrastructureIsRefused(t *testing.T) {
	out, _, matched := runAlert(t, alert{
		summary: "periodic outbound connections with fixed interval", confidence: 0.88,
		peerIP: "198.51.100.7", targetHost: "build-07",
	})
	if !matched {
		t.Fatal("expected the beacon to match a technique")
	}
	if _, executed := out["executed"]; executed {
		t.Fatal("blocking a shared CDN node must not run")
	}
	why, _ := out[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "payment callback") {
		t.Fatalf("the refusal should name what else it would break, got %q", why)
	}
}

func TestIsolatingCriticalInfrastructureIsRefused(t *testing.T) {
	for _, host := range []string{"jump01", "dc01", "coredb01", "vpn-gw01"} {
		out, _, matched := runAlert(t, alert{
			summary: "lateral movement over rdp", confidence: 0.91,
			peerIP: "10.4.2.19", targetHost: host,
		})
		if !matched {
			t.Fatalf("%s: expected a technique match", host)
		}
		if _, executed := out["executed"]; executed {
			t.Fatalf("%s is critical and must not be isolated automatically", host)
		}
	}
}

func TestIsolatingAnUnknownHostIsRefused(t *testing.T) {
	out, _, matched := runAlert(t, alert{
		summary: "lateral movement over rdp", confidence: 0.95,
		peerIP: "10.4.2.19", targetHost: "who-is-this",
	})
	if !matched {
		t.Fatal("expected a technique match")
	}
	if _, executed := out["executed"]; executed {
		t.Fatal("an unidentified host must not be isolated automatically")
	}
	why, _ := out[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "not in inventory") {
		t.Fatalf("expected the refusal to say the host is unknown, got %q", why)
	}
}

func TestLowConfidenceNeverReachesContainment(t *testing.T) {
	out, trace, matched := runAlert(t, alert{
		summary: "beaconing to an unclassified external host", confidence: 0.42,
		peerIP: "203.0.113.77", targetHost: "ws-4471",
	})
	if !matched {
		t.Fatal("the technique should still match; it is the confidence that is low")
	}
	if _, executed := out["executed"]; executed {
		t.Fatal("a low-confidence detection must not trigger containment")
	}
	if trace[len(trace)-1] != "queue_for_review" {
		t.Fatalf("expected the review queue, got %v", trace)
	}

	for _, step := range trace {
		if step == "select_response" || step == "contain" {
			t.Fatalf("a weak signal must not get as far as %q: %v", step, trace)
		}
	}
}

func TestUnrelatedAlertMatchesNothing(t *testing.T) {
	_, _, matched := runAlert(t, alert{
		summary: "printer firmware update failed twice", confidence: 0.99,
		peerIP: "10.1.1.5", targetHost: "ws-4471",
	})
	if matched {
		t.Fatal("a printer fault must not be matched to an ATT&CK technique")
	}
}

func TestEveryMatchableTechniqueHasAnSOP(t *testing.T) {
	router, _, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	for _, a := range feed() {
		match := router.Match("network-detection", a.summary)
		if !match.Matched {
			continue
		}
		if _, ok := containmentSOP[match.NodeRef]; !ok {
			t.Fatalf("technique %q can be matched but has no containment SOP", match.NodeRef)
		}
	}
}

func TestFeedCoversEveryDisposition(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range feed() {
		out, trace, matched := runAlert(t, a)
		if !matched {
			seen["unmatched"] = true
			continue
		}
		seen[trace[len(trace)-1]] = true
		_ = out
	}
	for _, want := range []string{"record_contained", "hold_for_analyst", "queue_for_review", "unmatched"} {
		if !seen[want] {
			t.Fatalf("the demo feed no longer exercises %q", want)
		}
	}
}
