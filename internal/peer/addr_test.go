package peer

import "testing"

func TestAnAddressIsAURIAndABareHostPortIsTCP(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:7700":        "tcp://127.0.0.1:7700",
		"tcp://127.0.0.1:7700":  "tcp://127.0.0.1:7700",
		"tcp://node.example:1/": "tcp://node.example:1",
		"[::1]:7700":            "tcp://[::1]:7700",
	} {
		uri, hp, err := ParseAddr(in)
		if err != nil || uri != want || "tcp://"+hp != want {
			t.Errorf("%q: %q %q %v", in, uri, hp, err)
		}
	}
	for _, in := range []string{"quic://127.0.0.1:1", "http://a:1", "tcp://nohost", "tcp://:80", "tcp://a:1/path", "tcp://u@a:1", "", "x"} {
		if uri, _, err := ParseAddr(in); err == nil {
			t.Errorf("%q was accepted as %q", in, uri)
		}
	}
}
