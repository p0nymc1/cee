package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The screener reads real sockets, but the tests read fixed text. A test that
// depends on whatever the machine happens to be doing proves nothing and fails
// for unrelated reasons.
const lsofSample = `COMMAND     PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
Chrome    36129 sorry   31u  IPv4 0xab7a21dfae81497a      0t0  TCP 192.168.0.5:51960->93.184.216.34:443 (ESTABLISHED)
oddjob      991 sorry   12u  IPv4 0x246f6005f9dd5600      0t0  TCP 192.168.0.5:52345->203.0.113.66:443 (ESTABLISHED)
legacyftp   777 sorry    9u  IPv4 0x5a691f47525ed1df      0t0  TCP 192.168.0.5:52245->198.51.100.9:21 (ESTABLISHED)
Chrome    36129 sorry   40u  IPv4 0x56a72c365f6b4944      0t0  TCP 192.168.0.5:52365->198.51.100.9:21 (ESTABLISHED)
postgres    440 sorry   14u  IPv4 0x578d97a3bbb0367e      0t0  TCP 127.0.0.1:5432->127.0.0.1:52247 (ESTABLISHED)
sshd        220 sorry    5u  IPv4 0x678d97a3bbb0367e      0t0  TCP 192.168.0.5:22->192.168.0.9:61122 (ESTABLISHED)
nginx       310 sorry    6u  IPv4 0x778d97a3bbb0367e      0t0  TCP *:443 (LISTEN)
`

func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func testPolicy() Policy {
	p := DefaultPolicy()
	p.RiskyPeers = map[string]string{"203.0.113.66": "from a phishing mail"}
	p.ExpectedTalkers = []string{"Chrome"}
	return p
}

func verdicts(t *testing.T) map[string]string {
	t.Helper()
	router, engine, err := buildRuntime(testPolicy())
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	match := router.Match("local-netwatch", "review established connections")
	if !match.Matched {
		t.Fatal("the screening intent should match")
	}

	out := map[string]string{}
	for _, c := range parseLsof(lsofSample) {
		line, err := screen(engine, match.EntryWorkflowRef, c)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		out[c.Process] = line
	}
	return out
}

func TestListeningSocketsAreNotConnections(t *testing.T) {
	for _, c := range parseLsof(lsofSample) {
		if c.Process == "nginx" {
			t.Fatal("a LISTEN row is not an outbound connection")
		}
	}
}

// Question one: is anything going somewhere it should not?
func TestPlaintextProtocolToTheInternetIsFlagged(t *testing.T) {
	got := verdicts(t)["legacyftp"]
	if !strings.HasPrefix(got, "FLAGGED") {
		t.Fatalf("FTP to a public address should be flagged, got %q", got)
	}
	if !strings.Contains(got, "clear") {
		t.Fatalf("the alert should say why it matters, got %q", got)
	}
}

// Question two: is anything talking to an address I marked risky?
func TestARiskyPeerIsFlagged(t *testing.T) {
	got := verdicts(t)["oddjob"]
	if !strings.HasPrefix(got, "FLAGGED") {
		t.Fatalf("a connection to a listed risky peer should be flagged, got %q", got)
	}
	if !strings.Contains(got, "phishing") {
		t.Fatalf("the alert should carry your own note, got %q", got)
	}
}

// The guardrail. Most outbound traffic on a laptop is a browser, and flagging
// all of it teaches people to ignore the screener within a day.
func TestAnExpectedTalkerIsSuppressedWithAReason(t *testing.T) {
	got := verdicts(t)["Chrome"]
	if strings.HasPrefix(got, "FLAGGED") {
		t.Fatalf("an expected talker on a noisy finding should be suppressed, got %q", got)
	}
	if !strings.Contains(got, "expected-talkers") {
		t.Fatalf("a suppression must say why, or it is indistinguishable from a miss: %q", got)
	}
}

// Suppression must never hide a risky peer: malware runs inside ordinary
// processes, so a familiar name is the wrong reason to stay quiet.
func TestBeingExpectedDoesNotSuppressARiskyPeer(t *testing.T) {
	router, engine, err := buildRuntime(testPolicy())
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	match := router.Match("local-netwatch", "review established connections")

	// Chrome is an expected talker, and this peer is on the risky list.
	line, err := screen(engine, match.EntryWorkflowRef,
		Conn{Process: "Chrome", PID: 1, PeerIP: "203.0.113.66", PeerPort: 443})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(line, "FLAGGED") {
		t.Fatalf("a risky peer must be raised whatever the process, got %q", line)
	}
}

// Traffic that never left the building is not an outbound risk, and screening
// it would bury the connections that did leave.
func TestInternalTrafficIsNotScreenedAsOutbound(t *testing.T) {
	got := verdicts(t)
	for _, process := range []string{"postgres", "sshd"} {
		if !strings.HasPrefix(got[process], "internal") {
			t.Fatalf("%s is internal traffic, got %q", process, got[process])
		}
	}
}

func TestPublicAddressClassification(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true,
		"8.8.8.8":       true,
		"192.168.0.5":   false,
		"10.1.2.3":      false,
		"172.16.0.1":    false,
		"127.0.0.1":     false,
		"169.254.1.1":   false,
		"not-an-ip":     false,
	} {
		if isPublic(addr) != want {
			t.Fatalf("isPublic(%q) should be %v", addr, want)
		}
	}
}

// A missing policy file is a starting point, not an error: you begin with
// nothing listed and add what you learn.
func TestAMissingPolicyFileIsNotAnError(t *testing.T) {
	policy, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing policy should fall back to the default: %v", err)
	}
	if len(policy.RiskyPeers) != 0 {
		t.Fatal("the default must not pretend to know which addresses are dangerous")
	}
	if len(policy.PlaintextPorts) == 0 {
		t.Fatal("the plaintext port list is a fact about protocols and should be present")
	}
}

func TestAMalformedPolicyFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netwatch.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Fatal("a broken policy file must be reported, not silently ignored")
	}
}
