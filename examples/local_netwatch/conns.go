package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// Conn is one established outbound connection as the operating system
// reports it.
type Conn struct {
	Process  string
	PID      int
	LocalIP  string
	PeerIP   string
	PeerPort int
}

func (c Conn) String() string {
	return fmt.Sprintf("%s[%d] -> %s:%d", c.Process, c.PID, c.PeerIP, c.PeerPort)
}

// Established reads the machine's current TCP connections.
//
// lsof rather than a packet capture: reading the socket table needs no
// elevated privileges, no interface in promiscuous mode and no third-party
// library, so this stays inside the module's zero-dependency rule and inside
// what an ordinary user can run on their own laptop.
//
// It is also the honest boundary of what this can see: established sockets,
// not payloads. Nothing here inspects traffic.
func Established() ([]Conn, error) {
	out, err := exec.Command("lsof", "-iTCP", "-sTCP:ESTABLISHED", "-n", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("reading the socket table with lsof: %w", err)
	}
	return parseLsof(string(out)), nil
}

// parseLsof pulls connections out of lsof's columnar output. Split out so the
// tests run on fixed input instead of on whatever the machine happens to be
// doing.
func parseLsof(out string) []Conn {
	var conns []Conn
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}
		// The address column looks like 192.168.0.2:52345->93.184.216.34:443
		addr := fields[8]
		local, peer, ok := strings.Cut(addr, "->")
		if !ok {
			continue // a listening socket, not a connection
		}
		localIP, _, ok := strings.Cut(local, ":")
		if !ok {
			continue
		}
		peerIP, peerPort, ok := strings.Cut(peer, ":")
		if !ok {
			continue
		}
		port, err := strconv.Atoi(peerPort)
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(fields[1])

		conns = append(conns, Conn{
			Process: fields[0], PID: pid,
			LocalIP: localIP, PeerIP: peerIP, PeerPort: port,
		})
	}
	return conns
}

// isPublic reports whether an address is outside this machine and its own
// networks. Everything private, loopback or link-local is traffic that never
// left the building, and screening it as an outbound risk would bury the
// connections that did.
func isPublic(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified()
}
