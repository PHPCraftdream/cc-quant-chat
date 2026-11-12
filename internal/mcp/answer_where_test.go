package mcp

import (
	"strings"
	"testing"
)

// An agent's plain text reaches only its own user; another agent hears only chat_send. The words that say so
// must not be lost when the texts are edited.
func TestTheTextsTellAnAgentThatOnlyChatSendReachesAnotherAgent(t *testing.T) {
	rules := instructions[strings.Index(instructions, "Rules:"):]
	first := strings.SplitN(rules, "\n", 3)[1] // the first rule
	for _, want := range []string{
		"Your plain text reaches only your user.",
		"Another agent receives only what you send with chat_send.",
		"answer it with chat_send to its reply address; do not answer in plain text.",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the first rule lacks %q:\n%s", want, first)
		}
	}
	if !strings.Contains(rules, "a question or a request to you does") {
		t.Error("the rule against empty acks must say that a question is answered")
	}

	var send string
	for _, d := range strings.Split(string(toolDefs), "\n") {
		if strings.HasPrefix(d, `{"name":"chat_send"`) {
			send = d
		}
	}
	if !strings.Contains(send, "the only way to reach another member: your normal reply text is not delivered to the chat") {
		t.Errorf("chat_send does not say that it is the only way: %s", send)
	}

	head := renderBatch([]pendItem{{line: "[/team/@common #1 2026-01-01T00:00:00Z] bob → @alice: hi   (reply: /team/@direct/@bob)"}})
	head = head[:strings.Index(head, "\n")]
	for _, want := range []string{
		"A question or a request to you must be answered with chat_send to the address in (reply: ...)",
		"your plain text reaches only your user",
		"Information only: no answer needed.",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("the wake-up header lacks %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "only if a reply is needed") {
		t.Errorf("the header still tells the agent not to answer: %s", head)
	}
}
