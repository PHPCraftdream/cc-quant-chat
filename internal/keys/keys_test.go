package keys

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func secret(t *testing.T) []byte {
	t.Helper()
	s, err := NewSecret()
	if err != nil || len(s) != SecretSize {
		t.Fatalf("%v", err)
	}
	return s
}

func TestTheNickKeyIsTheSameForTheSameInputsAndNeedsBothSecrets(t *testing.T) {
	a, b := secret(t), secret(t)
	k1, err := SignKey(a, b, "chat1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := SignKey(a, b, "chat1", "alice")
	if !k1.Equal(k2) {
		t.Fatal("the key must be reproducible from A and B")
	}
	if other, _ := SignKey(secret(t), b, "chat1", "alice"); other.Equal(k1) {
		t.Fatal("another A gave the same key")
	}
	if other, _ := SignKey(a, secret(t), "chat1", "alice"); other.Equal(k1) {
		t.Fatal("another B gave the same key")
	}
	if other, _ := SignKey(b, a, "chat1", "alice"); other.Equal(k1) {
		t.Fatal("swapping A and B gave the same key")
	}
}

func TestEveryChatAndNickHasItsOwnKeysAndTheAuthKeyIsNotTheNickKey(t *testing.T) {
	a, b := secret(t), secret(t)
	base, _ := SignKey(a, b, "chat1", "alice")
	for _, c := range []struct{ chat, nick string }{{"chat2", "alice"}, {"chat1", "bob"}, {"chat1alice", ""}, {"chat", "1alice"}} {
		if k, _ := SignKey(a, b, c.chat, c.nick); k.Equal(base) {
			t.Fatalf("%+v: the key repeats", c)
		}
	}
	au, _ := AuthKey(a, "chat1", "alice")
	if au.Equal(base) || bytes.Equal(au.Public().(ed25519.PublicKey), base.Public().(ed25519.PublicKey)) {
		t.Fatal("the auth key must differ from the nick key")
	}
	if other, _ := AuthKey(a, "chat1", "bob"); other.Equal(au) {
		t.Fatal("the auth key must be its own for every nick")
	}
	if again, _ := AuthKey(a, "chat1", "alice"); !again.Equal(au) {
		t.Fatal("the auth key must be reproducible from A")
	}
}

func TestTheNickKeyCannotBeTakenFromAOrBAlone(t *testing.T) {
	a, b := secret(t), secret(t)
	k, _ := SignKey(a, b, "c", "n")
	for name, s := range map[string][]byte{"A": a, "B": b} {
		if ed25519.NewKeyFromSeed(s).Equal(k) {
			t.Fatalf("the seed of the nick key is %s", name)
		}
		au, _ := AuthKey(s, "c", "n")
		if au.Equal(k) {
			t.Fatalf("a key derived from %s alone is the nick key", name)
		}
	}
}

func TestASecretOfAWrongSizeIsRefused(t *testing.T) {
	if _, err := AuthKey([]byte("short"), "c", "n"); err == nil {
		t.Fatal("a short A was taken")
	}
	if _, err := SignKey(secret(t), nil, "c", "n"); err == nil {
		t.Fatal("a missing B was taken")
	}
}

func TestWipeZeroesTheSecret(t *testing.T) {
	s := secret(t)
	Wipe(s)
	if !bytes.Equal(s, make([]byte, SecretSize)) {
		t.Fatal("not wiped")
	}
}
