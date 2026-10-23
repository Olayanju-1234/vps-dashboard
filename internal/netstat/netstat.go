// Package netstat lists listening TCP sockets from /proc/net/tcp{,6}. Those
// files are world-readable, so no privilege is needed; they give the bind
// address and owning uid but not the process.
package netstat

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Listener is one listening socket.
type Listener struct {
	IP   net.IP
	Port int
	UID  uint32
}

// Loopback reports whether the socket only accepts local connections.
func (l Listener) Loopback() bool { return l.IP.IsLoopback() }

const stateListen = "0A"

// Parse reads one /proc/net/tcp or tcp6 table.
func Parse(r io.Reader) ([]Listener, error) {
	var out []Listener
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[3] != stateListen {
			continue
		}
		ip, port, err := parseAddr(f[1])
		if err != nil {
			return nil, err
		}
		uid, err := strconv.ParseUint(f[7], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("netstat: uid %q: %w", f[7], err)
		}
		out = append(out, Listener{IP: ip, Port: port, UID: uint32(uid)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("netstat: read: %w", err)
	}
	return out, nil
}

// parseAddr decodes "0100007F:1F90": the address is in host byte order per
// 32-bit word (little-endian on x86 and arm64), the port is big-endian hex.
func parseAddr(s string) (net.IP, int, error) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("netstat: address %q", s)
	}
	raw, err := hex.DecodeString(a)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, fmt.Errorf("netstat: address %q", s)
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("netstat: port %q: %w", p, err)
	}
	return net.IP(raw), int(port), nil
}

// Listeners reads both tables under a /proc root.
func Listeners(procRoot string) ([]Listener, error) {
	var all []Listener
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("netstat: %w", err)
		}
		ls, err := Parse(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, ls...)
	}
	return all, nil
}
