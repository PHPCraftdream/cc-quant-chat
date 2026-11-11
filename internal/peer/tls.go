// Package peer exchanges the journals of chats between nodes (docs/design-federation.md): TLS 1.3
// between two brokers that know each other, the exchange of what each lacks, and the checks every
// event passes before it is kept.
package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
)

// A node is its key. The certificate a node shows is self-signed with the node key; nobody checks
// a name or a chain, they check that the key in it is the one they expect: its origin (the first
// 16 bytes of its hash) is what the owner typed in `ccqc peer add`.

func certFor(priv ed25519.PrivateKey) (tls.Certificate, error) {
	pub := priv.Public().(ed25519.PublicKey)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ccqc-" + node.Origin(pub)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

// originCheck decides whether to talk to the node whose key was shown.
type originCheck func(origin string, pub ed25519.PublicKey) error

// tlsConfig: TLS 1.3, our certificate, and the other side's checked by its key alone, on both sides
// (the server asks for a certificate from the client too).
func tlsConfig(priv ed25519.PrivateKey, server bool, check originCheck) (*tls.Config, error) {
	cert, err := certFor(priv)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		// There is no name to verify and no authority: the identity is the key, checked below.
		InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate does the verification
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the node showed no certificate")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			pub, ok := c.PublicKey.(ed25519.PublicKey)
			if !ok {
				return errors.New("the certificate does not hold an Ed25519 key")
			}
			return check(node.Origin(pub), pub)
		},
	}
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
	}
	return cfg, nil
}

// peerKey is the key the other side showed in a finished handshake.
func peerKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("no certificate")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the certificate does not hold an Ed25519 key")
	}
	return pub, nil
}
