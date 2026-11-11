package local

import (
	"crypto/ed25519"
	"sync"
	"testing"
)

func TestNickKeyIsCreatedOnceKeptNextToTheSecretAndSharedBySessions(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	if err := SetNickKey("chat1", "alice", "old-secret"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	keys := make([]ed25519.PrivateKey, 8)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := NickPrivateKey("chat1", "alice")
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		}()
	}
	wg.Wait()
	for _, k := range keys {
		if len(k) != ed25519.PrivateKeySize || !k.Equal(keys[0]) {
			t.Fatal("sessions asking at once must get one key")
		}
	}
	if got := NickKey("chat1", "alice"); got != "old-secret" {
		t.Fatalf("the old secret was lost: %q", got)
	}
	other, err := NickPrivateKey("chat1", "bob")
	if err != nil || other.Equal(keys[0]) {
		t.Fatalf("another nick must have its own key (%v)", err)
	}
	again, err := NickPrivateKey("chat1", "alice")
	if err != nil || !again.Equal(keys[0]) {
		t.Fatalf("the key must persist: %v", err)
	}
}
