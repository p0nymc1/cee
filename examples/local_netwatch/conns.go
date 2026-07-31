package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

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

func Established() ([]Conn, error) {
	out, err := exec.Command("lsof", "-iTCP", "-sTCP:ESTABLISHED", "-n", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("reading the socket table with lsof: %w", err)
	}
	return parseLsof(string(out)), nil
}

func parseLsof(out string) []Conn {
	var conns []Conn
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}

		addr := fields[8]
		local, peer, ok := strings.Cut(addr, "->")
		if !ok {
			continue
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

func isPublic(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified()
}
