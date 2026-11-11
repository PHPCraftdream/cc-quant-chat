package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// A chat_send survives a broker restart: it waits for the reconnect and repeats the request with
// the same client_msg_id, so the broker stores and delivers the message once.
var (
	sendWindow = 30 * time.Second
	sendDelay  = 300 * time.Millisecond
)

func newClientMsgID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// retryable: a lost connection, a timeout or a restarting broker. Any other answer of the
// broker (rate_limited, not_in_room, too_large, ...) is final.
func retryable(err error) bool {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code == "restarting"
	}
	return true
}

// withRetry runs call until it succeeds, fails for good or the window ends.
func withRetry(call func() error, window, delay time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		err := call()
		if err == nil || !retryable(err) {
			return err
		}
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("not sent: the broker did not come back within %s (%v). "+
				"If the message did get through, sending it again would show it twice", window, err)
		}
		time.Sleep(delay)
	}
}
