package peer

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseAddr reads the address of a node. An address is a URI whose scheme names the carrier of the
// link; for now there is one, tcp://host:port, and a bare host:port is taken as that. It returns the
// URI in its stored form and the host:port to dial. Nothing else about the address is used: the
// link's identity is the node's key, so what lies behind the address (a tunnel to a loopback port,
// say) does not matter.
func ParseAddr(s string) (uri, hostport string, err error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "tcp://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("address %q is not a URI: %v", s, err)
	}
	if u.Scheme != "tcp" {
		return "", "", fmt.Errorf("unsupported address scheme %q in %q (supported: tcp://host:port)", u.Scheme, s)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("address %q: tcp://host:port and nothing else", s)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("address %q: tcp://host:port, with a port", s)
	}
	hostport = net.JoinHostPort(host, port)
	return "tcp://" + hostport, hostport, nil
}
