package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A broker older than proto 4 knows no "challenge" (or ignores the key): the join goes by the
// secret and the client does not sign.
func TestJoinSignedAgainstAnOlderBrokerFallsBackToTheSecretAndDoesNotSign(t *testing.T) {
	for name, challengeErr := range map[string]error{
		"no challenge op":      &proto.Error{Code: "not_joined", Msg: "join first"},
		"unknown op":           &proto.Error{Code: "unknown_op"},
		"challenge but proto3": nil,
	} {
		var joined proto.JoinArgs
		call := func(op string, args, out any) error {
			switch op {
			case "challenge":
				if challengeErr != nil {
					return challengeErr
				}
				*out.(*proto.Challenge) = proto.Challenge{Nonce: "n"}
			case "join":
				joined = args.(proto.JoinArgs)
				*out.(*proto.Welcome) = proto.Welcome{Proto: 3, Key: "issued-secret"}
			}
			return nil
		}
		var w proto.Welcome
		sg, err := JoinSigned(call, proto.JoinArgs{Token: "t", Name: "alice", Key: "secret"}, key(t), "chat", &w)
		if err != nil || sg != nil || w.Key != "issued-secret" {
			t.Fatalf("%s: signer %v, err %v, key %q", name, sg, err, w.Key)
		}
		if joined.Key != "secret" {
			t.Fatalf("%s: the secret was not presented: %+v", name, joined)
		}
		if challengeErr != nil && joined.Pub != "" {
			t.Fatalf("%s: a key was presented without a challenge", name)
		}
	}
}

func TestJoinSignedRefusalOfTheBrokerIsReturned(t *testing.T) {
	call := func(op string, args, out any) error {
		if op == "challenge" {
			return nil
		}
		return &proto.Error{Code: "name_taken"}
	}
	var w proto.Welcome
	if _, err := JoinSigned(call, proto.JoinArgs{Name: "alice"}, key(t), "chat", &w); !proto.IsCode(err, "name_taken") {
		t.Fatalf("err = %v", err)
	}
	broken := func(op string, args, out any) error { return errors.New("pipe closed") }
	if _, err := JoinSigned(broken, proto.JoinArgs{Name: "alice"}, key(t), "chat", &w); err == nil {
		t.Fatal("a broken connection must not turn into a legacy join")
	}
}

// Write signs again when another write took the clock value, takes the new value from the error
// without asking for the clock, and gives up after maxSignTries.
func TestWriteResignsOnAStaleClockFromTheErrorAndGivesUp(t *testing.T) {
	sg := &Signer{Priv: key(t), ChatID: "chat", Origin: "node"}
	clocks, sends := 0, 0
	var lamports []int64
	call := func(op string, args, out any) error {
		switch op {
		case "clock":
			clocks++
			*out.(*proto.ClockReply) = proto.ClockReply{Next: 5}
		case "send":
			sends++
			a := args.(*proto.SendArgs)
			lamports = append(lamports, a.Lamport)
			if sends < 3 {
				data, _ := json.Marshal(proto.ClockReply{Next: a.Lamport + 2})
				return &proto.Error{Code: "stale_clock", Data: data}
			}
		}
		return nil
	}
	if err := sg.Write(call, "send", "msg", &proto.SendArgs{Room: "common", Text: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if clocks != 1 || sends != 3 || lamports[0] != 5 || lamports[1] != 7 || lamports[2] != 9 {
		t.Fatalf("clock asked %d times, %d sends at %v", clocks, sends, lamports)
	}

	sends = 0
	always := func(op string, args, out any) error {
		if op == "clock" {
			return nil
		}
		sends++
		data, _ := json.Marshal(proto.ClockReply{Next: int64(sends) + 1})
		return &proto.Error{Code: "stale_clock", Data: data}
	}
	err := sg.Write(always, "send", "msg", &proto.SendArgs{Room: "common", Text: "x"}, nil)
	if !proto.IsCode(err, "stale_clock") || sends != maxSignTries {
		t.Fatalf("err %v after %d tries, want stale_clock after %d", err, sends, maxSignTries)
	}
}

// What the client signs is what the broker rebuilds from the wire: the canonical request without
// origin, lamport and sig.
func TestSignedBytesAreTheCanonicalRequestWithoutTheSignedFields(t *testing.T) {
	sg := &Signer{Priv: key(t), ChatID: "chat", Origin: "node"}
	a := &proto.SendArgs{Room: "common", Text: "a <b> & c", ClientMsgID: "cid"}
	if err := sg.Sign("msg", a, 7); err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(a)
	sp, err := proto.SplitSigned(wire)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Origin != "node" || sp.Lamport != 7 || sp.Sig == "" {
		t.Fatalf("signed fields: %+v", sp)
	}
	// Go's encoder writes <, > and & as backslash-u escapes.
	bs := string(rune(92))
	want := `{"client_msg_id":"cid","room":"common","text":"a ` + bs + `u003cb` + bs + `u003e ` + bs + `u0026 c"}`
	if string(sp.Canon) != want {
		t.Fatalf("canon %s, want %s", sp.Canon, want)
	}
	msg := proto.EventMessage("chat", "node", "msg", sp.Canon, 7)
	sig := ed25519.Sign(sg.Priv, msg)
	if !ed25519.Verify(sg.Priv.Public().(ed25519.PublicKey), msg, sig) {
		t.Fatal("sanity")
	}
}
