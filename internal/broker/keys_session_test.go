package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const (
	sidA = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	sidB = "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	sidC = "33333333-cccc-4ccc-8ccc-cccccccccccc"
	sidD = "44444444-dddd-4ddd-8ddd-dddddddddddd"
)

// session enrolls a protected nick from a session and leaves the chat, so it can be logged in again.
func (ic *invitedChat) session(t *testing.T, nick, sid string) []byte {
	t.Helper()
	a := secretOf(t)
	ic.sid = sid
	c, _, _ := ic.enroll(t, nick, a)
	ic.leave(t, c, nick)
	return a
}

// adminLog is the administrator log as "op arg-text: result" lines.
func adminLog(t *testing.T, ic *invitedChat) []string {
	t.Helper()
	raw, err := sqlite.OpenRaw(ic.path[:strings.LastIndexAny(ic.path, `/\`)] + "/node.db")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT op, args, result FROM admin_log ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op, args, res string
		rows.Scan(&op, &args, &res)
		out = append(out, op+" "+args+": "+res)
	}
	return out
}

func lastOf(lines []string, op string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], op+" ") {
			return lines[i]
		}
	}
	return ""
}

func TestTheSameSessionPassesAndAnotherOneIsRefusedAndToldToTheChat(t *testing.T) {
	ic := newInvitedChat(t)
	a := ic.session(t, "bob", sidA)
	watch := ic.watcher(t)

	// resume: the same id.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidA, "", ""); code != "" {
		t.Fatalf("the same session: %q", code)
	}
	// Another session (a new one, a fork: no link to the old id): refused, with a way out.
	for name, args := range map[string][3]string{
		"another":                  {sidB, "", ""},
		"a fork without a link":    {sidB, "", "resume"},
		"no id at all":             {"", "", ""},
		"a link of the wrong kind": {sidB, sidA, "resume"},
		"a link to another id":     {sidB, sidC, "clear"},
	} {
		if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, args[0], args[1], args[2]); code != "unlock_session" {
			t.Fatalf("%s: %q", name, code)
		}
	}
	if heard := watch.heard(t, "from another session", 3*time.Second); !strings.Contains(heard, "@bob") || !strings.Contains(heard, "refused") {
		t.Fatalf("the chat was not told: %q", heard)
	}
	// Told once for all those attempts, not every time.
	if more := watch.heard(t, "from another session", 300*time.Millisecond); more != "" {
		t.Fatalf("told again: %q", more)
	}
	// Nothing was handed out and the binding is as it was.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidA, "", ""); code != "" {
		t.Fatalf("the original session after the refusals: %q", code)
	}
	log := adminLog(t, ic)
	refused := 0
	for _, l := range log {
		if strings.Contains(l, "refused: ") && strings.Contains(l, `"peer"`) { // another session, and no id at all
			refused++
		}
	}
	if refused != 5 {
		t.Fatalf("%d refusals in the log: %v", refused, log)
	}
}

func TestAfterClearOrACompactionTheBindingMovesOneStepAlongTheChain(t *testing.T) {
	ic := newInvitedChat(t)
	a := ic.session(t, "bob", sidA)
	watch := ic.watcher(t)

	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidB, sidA, "clear"); code != "" {
		t.Fatalf("clear: %q", code)
	}
	heard := watch.heard(t, "changed its id", 3*time.Second)
	if !strings.Contains(heard, "@bob") || !strings.Contains(heard, "clear") {
		t.Fatalf("the chat was not told: %q", heard)
	}
	// The old id no longer opens it; the new one does; the step is in the administrator log.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidA, "", ""); code != "unlock_session" {
		t.Fatalf("the old id after the move: %q", code)
	}
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidB, "", ""); code != "" {
		t.Fatalf("the new id: %q", code)
	}
	if l := lastOf(adminLog(t, ic), "key.rebind"); !strings.Contains(l, `"prev":"11111111…"`) || !strings.Contains(l, `"sid":"22222222…"`) || !strings.Contains(l, `"source":"clear"`) {
		t.Fatalf("log: %q", l)
	}
	// A second step in a row (a compaction): from the id it has now.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidC, sidB, "compact"); code != "" {
		t.Fatalf("compact: %q", code)
	}
	// Only the id the broker has counts, not "one of the earlier ones".
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidD, sidA, "clear"); code != "unlock_session" {
		t.Fatalf("an earlier id as the link: %q", code)
	}
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidD, sidB, "clear"); code != "unlock_session" {
		t.Fatalf("an earlier id as the link: %q", code)
	}
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidD, sidC, "clear"); code != "" {
		t.Fatalf("the third step: %q", code)
	}
}

func TestTheBindingMovesOnlyForTheHolderOfAAndOnlyForANickThatIsOffline(t *testing.T) {
	ic := newInvitedChat(t)
	a := ic.session(t, "bob", sidA)
	// Without A (a wrong proof) the chain moves nothing, whatever it says.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", secretOf(t), sidB, sidA, "clear"); code != "unlock_denied" {
		t.Fatalf("a wrong proof: %q", code)
	}
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidA, "", ""); code != "" {
		t.Fatalf("the binding moved without A: %q", code)
	}
	// A nick that is present is not asked about at all.
	c, _, code := ic.login(t, "bob", a)
	if code != "" {
		t.Fatal(code)
	}
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidB, sidA, "clear"); code != "name_online" {
		t.Fatalf("an online nick: %q", code)
	}
	ic.leave(t, c, "bob")
	if _, code := ic.unlockIn(t, ic.b.connect(t), "bob", a, sidB, sidA, "clear"); code != "" {
		t.Fatalf("offline again: %q", code)
	}
}

// A protected nick belongs to a session. A terminal has none: an enrolment without a session id is refused (and the
// invitation is not spent), a key is not given for no id, and a nick that an earlier version enrolled with no session
// (the terminal nick "cli") is not given its key either, whatever the emergency switch says: that switch lets another
// session in, it does not let no session in.
func TestANickWithNoSessionIsNeitherEnrolledNorGivenItsKeyAndTheEmergencySwitchOnlyLetsAnotherSessionIn(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)

	in := ic.issue(t, "cli")
	c := ic.b.connect(t)
	var rep proto.KeyPrepareReply
	if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "cli"}, &rep); code != "" {
		t.Fatalf("key_prepare: %q", code)
	}
	ic.sid = ""
	if _, code := ic.enrollJoin(t, c, "cli", a, b64(t, rep.B), &in); code != "session_required" {
		t.Fatalf("an enrolment from a terminal: %q, want session_required", code)
	}
	// The invitation was not spent: from a session it enrols.
	ic.sid = sidA
	c2 := ic.b.connect(t)
	if code := c2.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "cli"}, &rep); code != "" {
		t.Fatalf("key_prepare: %q", code)
	}
	if _, code := ic.enrollJoin(t, c2, "cli", a, b64(t, rep.B), &in); code != "" {
		t.Fatalf("the same invitation from a session: %q", code)
	}
	ic.leave(t, c2, "cli")

	// The key is not given for no session id, with the binding on or off.
	for _, state := range []string{"on", "off"} {
		adminSet(t, ic, "keys.bind_session", state)
		if _, code := ic.unlockIn(t, ic.b.connect(t), "cli", a, "", "", ""); code != "unlock_session" {
			t.Fatalf("no session id, binding %s: %q", state, code)
		}
	}
	// With the emergency switch off another session is let in (and told to the chat), as before.
	if _, code := ic.unlockIn(t, ic.b.connect(t), "cli", a, sidB, "", ""); code != "" {
		t.Fatalf("another session, binding off: %q", code)
	}
	adminSet(t, ic, "keys.bind_session", "on")

	// A nick that an earlier version enrolled with no session: its row has no binding.
	ss, ok := ic.b.nickSecrets()
	if !ok {
		t.Fatal("no secrets")
	}
	if _, err := ss.NickSecretRebind(ic.chat, "cli", ""); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"on", "off"} {
		adminSet(t, ic, "keys.bind_session", state)
		for _, sid := range []string{"", sidA, sidB} {
			if _, code := ic.unlockIn(t, ic.b.connect(t), "cli", a, sid, "", ""); code != "unlock_session" {
				t.Fatalf("a nick bound to no session, binding %s, session %q: %q", state, sid, code)
			}
		}
	}
	log := strings.Join(adminLog(t, ic), " | ")
	if !strings.Contains(log, "refused: no session id given") || !strings.Contains(log, "refused: the nick is bound to no session") {
		t.Fatalf("the refusals are not in the administrator log: %s", log)
	}
}

func TestTheAdministratorLogNamesTheProgramAndPidOfTheClientThatTheKernelReports(t *testing.T) {
	ic := newInvitedChat(t)
	a := ic.session(t, "bob", sidA)
	before := map[*conn]bool{}
	ic.b.mu.Lock()
	for c := range ic.b.conns {
		before[c] = true
	}
	ic.b.mu.Unlock()
	cl := ic.b.connect(t)
	set := false
	for i := 0; i < 200 && !set; i++ { // the broker registers the connection in its own goroutine
		ic.b.mu.Lock()
		for c := range ic.b.conns {
			if !before[c] {
				c.peer, set = ipc.Peer{PID: 4242, Image: "/opt/some/dir/ccqc"}, true
			}
		}
		ic.b.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if _, code := ic.unlockIn(t, cl, "bob", a, sidA, "", ""); code != "" {
		t.Fatal(code)
	}
	l := lastOf(adminLog(t, ic), "key.unlock")
	if !strings.Contains(l, "ccqc pid 4242") || strings.Contains(l, "/opt/some/dir") {
		t.Fatalf("the log must name the program and the pid, not the path: %q", l)
	}
}

func adminSet(t *testing.T, ic *invitedChat, key, value string) {
	t.Helper()
	if _, code := adminCall(t, ic.adm, ic.ownk, "config_set", proto.AdminConfig{Key: key, Value: value}); code != "" {
		t.Fatalf("config set %s: %q", key, code)
	}
}

// watch is a member of the chat that hears what the broker announces.
type watch struct {
	c *client
}

func (ic *invitedChat) watcher(t *testing.T) *watch {
	t.Helper()
	in := ic.issue(t, "watch")
	c := ic.b.connect(t)
	if _, code := ic.join(t, c, "watch", newKey(t), &in); code != "" {
		t.Fatalf("watcher: %q", code)
	}
	return &watch{c: c}
}

// heard waits up to d for a notice with the text in it and returns its whole text ("" if none came).
func (w *watch) heard(t *testing.T, contains string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-w.c.events:
			if ev.Kind == "notice" && strings.Contains(ev.Text, contains) {
				return ev.Text
			}
		case <-deadline:
			return ""
		}
	}
}
