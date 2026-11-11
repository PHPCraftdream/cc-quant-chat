package invite_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

var chatID = strings.Repeat("ab", 32)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func chatInvite(t *testing.T, ttl time.Duration) invite.Invitation {
	t.Helper()
	in, err := invite.New(invite.KindChat, ttl, t0, func(i *invite.Invitation) { i.ChatID, i.Chat, i.Nick = chatID, "team", "bob" })
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func store(t *testing.T) invite.SQLStore {
	t.Helper()
	db, err := sqlite.OpenRaw(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(invite.Schema); err != nil {
		t.Fatal(err)
	}
	return invite.SQLStore{DB: db}
}

func TestAnInvitationSurvivesTheStringAndATypoIsSeenAtOnce(t *testing.T) {
	in := chatInvite(t, 0)
	s := in.String()
	if !strings.HasPrefix(s, "ccqc1.") {
		t.Fatal(s)
	}
	got, err := invite.Parse("  " + s + "\n")
	if err != nil || got.ID() != in.ID() || got.Nick != "bob" || got.ChatID != chatID || got.Chat != "team" || got.Exp != t0.Add(invite.DefaultTTL).Unix() {
		t.Fatalf("%+v %v", got, err)
	}
	if !bytes.Equal(got.Seed, in.Seed) {
		t.Fatal("the secret did not come through")
	}
	flip := func(s string, i int) string {
		c := byte('0')
		if s[i] == c {
			c = '1'
		}
		return s[:i] + string(c) + s[i+1:]
	}
	for _, bad := range []string{"", "ccqc1.", "ccqc1.abc.def", flip(s, len(s)-1), strings.Replace(s, "ccqc1.", "ccqc2.", 1), flip(s, 10)} {
		if _, err := invite.Parse(bad); !errors.Is(err, invite.ErrFormat) {
			t.Errorf("%q was taken: %v", bad, err)
		}
	}
	if _, err := invite.New(invite.KindChat, time.Second, t0, nil); err == nil {
		t.Error("an invitation that lives a second")
	}
	if _, err := invite.New(invite.KindChat, 8*24*time.Hour, t0, nil); err == nil {
		t.Error("an invitation that lives eight days")
	}
}

func TestAProofIsGoodOnlyForItsConnectionAndItsPurposeAndOnlyFromTheHolder(t *testing.T) {
	in, other := chatInvite(t, 0), chatInvite(t, 0)
	pub := in.Public()
	sig := in.Proof([]byte("nonce-1"), []byte("chat|bob|key"))
	if !invite.Verify(pub, []byte("nonce-1"), []byte("chat|bob|key"), sig) {
		t.Fatal("a right proof was refused")
	}
	for name, ok := range map[string]bool{
		"another connection": invite.Verify(pub, []byte("nonce-2"), []byte("chat|bob|key"), sig),
		"another purpose":    invite.Verify(pub, []byte("nonce-1"), []byte("chat|eve|key"), sig),
		"the two shifted":    invite.Verify(pub, []byte("nonce-1c"), []byte("hat|bob|key"), sig),
		"another invitation": invite.Verify(other.Public(), []byte("nonce-1"), []byte("chat|bob|key"), sig),
		"no signature":       invite.Verify(pub, []byte("nonce-1"), []byte("chat|bob|key"), nil),
	} {
		if ok {
			t.Errorf("a proof was taken for %s", name)
		}
	}
}

func redeem(st invite.Store, in invite.Invitation, now time.Time, binding string) (invite.Record, error) {
	sig := in.Proof([]byte(binding), []byte("subject"))
	return invite.Redeem(st, in.ID(), now, "bobs-key", func(r invite.Record) bool {
		return invite.Verify(r.Pub, []byte(binding), []byte("subject"), sig)
	})
}

func TestAnInvitationWorksOnceAndNeverAfterItsTime(t *testing.T) {
	st := store(t)
	in := chatInvite(t, 0)
	if err := st.Insert(invite.NewRecord(in, "bob", "owner-key", t0)); err != nil {
		t.Fatal(err)
	}
	rec, err := redeem(st, in, t0.Add(time.Minute), "n1")
	if err != nil || rec.State != invite.StateUsed || rec.UsedBy != "bobs-key" || rec.Scope != "bob" {
		t.Fatalf("%+v %v", rec, err)
	}
	if _, err := redeem(st, in, t0.Add(2*time.Minute), "n2"); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("used twice: %v", err)
	}
	late := chatInvite(t, 0)
	if err := st.Insert(invite.NewRecord(late, "bob", "owner-key", t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := redeem(st, late, t0.Add(invite.DefaultTTL), "n1"); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("an expired invitation: %v", err)
	}
	if _, err := redeem(st, chatInvite(t, 0), t0, "n1"); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("an unknown one: %v", err)
	}
}

func TestThreeBadProofsBurnAnInvitationAndTheFourthEvenIfRightIsRefused(t *testing.T) {
	st := store(t)
	in := chatInvite(t, 0)
	if err := st.Insert(invite.NewRecord(in, "bob", "owner-key", t0)); err != nil {
		t.Fatal(err)
	}
	wrong := func() error {
		_, err := invite.Redeem(st, in.ID(), t0, "k", func(invite.Record) bool { return false })
		return err
	}
	for i := 1; i <= invite.MaxTries; i++ {
		var bp *invite.BadProof
		err := wrong()
		if !errors.As(err, &bp) || !errors.Is(err, invite.ErrInvalid) || bp.Tries != i || bp.Burnt != (i == invite.MaxTries) || bp.Record.Scope != "bob" {
			t.Fatalf("bad proof %d: %v %+v", i, err, bp)
		}
	}
	if _, err := redeem(st, in, t0, "n1"); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("a burnt invitation: %v", err)
	}
	if r, _, _ := st.Get(in.ID()); r.State != invite.StateBurnt {
		t.Fatalf("state %q", r.State)
	}
}

func TestOfManyClientsWithOneInvitationOneGetsIn(t *testing.T) {
	st := store(t)
	in := chatInvite(t, 0)
	if err := st.Insert(invite.NewRecord(in, "bob", "owner-key", t0)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := redeem(st, in, t0.Add(time.Second), "nonce"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			} else if !errors.Is(err, invite.ErrInvalid) {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d clients got in with one invitation", won)
	}
}

func TestARevokedInvitationAndTheListShowNothingSecret(t *testing.T) {
	st := store(t)
	a, b := chatInvite(t, 0), chatInvite(t, 0)
	for _, in := range []invite.Invitation{a, b} {
		if err := st.Insert(invite.NewRecord(in, "bob", "owner-key", t0)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := st.Revoke(a.ID()); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := redeem(st, a, t0, "n1"); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("a revoked invitation: %v", err)
	}
	list, err := st.List(invite.KindChat)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	for _, r := range list {
		if bytes.Contains([]byte(r.ID+string(r.Pub)+r.Scope+r.IssuedBy), a.Seed) || bytes.Contains([]byte(r.ID+string(r.Pub)), b.Seed) {
			t.Fatal("a record holds the secret")
		}
	}
}
