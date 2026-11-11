package mcp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestSendIsRepeatedUntilTheBrokerIsBack(t *testing.T) {
	calls := 0
	err := withRetry(func() error {
		calls++
		switch calls {
		case 1:
			return errors.New("broker connection closed")
		case 2:
			return &proto.Error{Code: "restarting"}
		}
		return nil
	}, time.Second, time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("err %v after %d calls; want success on the third", err, calls)
	}
}

func TestPermanentSendErrorsAreNotRepeated(t *testing.T) {
	for _, code := range []string{"rate_limited", "not_in_room", "too_large", "no_such_room"} {
		calls := 0
		err := withRetry(func() error { calls++; return &proto.Error{Code: code} }, time.Second, time.Millisecond)
		if calls != 1 || !proto.IsCode(err, code) {
			t.Fatalf("%s: %d calls, err %v; want one call and the broker's error", code, calls, err)
		}
	}
}

func TestSendGivesUpWithAClearError(t *testing.T) {
	err := withRetry(func() error { return errors.New("down") }, 30*time.Millisecond, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("err = %v; want a \"not sent\" error", err)
	}
}

func TestClientMsgIDsDiffer(t *testing.T) {
	if a, b := newClientMsgID(), newClientMsgID(); a == b || len(a) < 16 {
		t.Fatalf("ids %q and %q", a, b)
	}
}
