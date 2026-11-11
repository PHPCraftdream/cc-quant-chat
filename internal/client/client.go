// Package client: broker connection with request/response correlation and an event stream.
package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

var ErrClosed = errors.New("broker connection closed")

const callTimeout = 10 * time.Second

type Conn struct {
	nc      net.Conn
	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan proto.Frame
	Events  chan proto.Event
	done    chan struct{}
	once    sync.Once
}

// Dial connects to a running broker; it never starts one.
func Dial() (*Conn, error) {
	nc, err := ipc.Dial(time.Second)
	if err != nil {
		return nil, err
	}
	c := &Conn{
		nc:      nc,
		pending: map[int64]chan proto.Frame{},
		Events:  make(chan proto.Event, 4096),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Connect dials the broker, starting it if it isn't running.
func Connect() (*Conn, error) { return ConnectWaiting(0) }

// ConnectWaiting first waits up to wait for a broker to appear (e.g. one that
// `ccqc broker restart` is starting) and only then starts one itself.
func ConnectWaiting(wait time.Duration) (*Conn, error) {
	if c, err := WaitDial(wait); err == nil {
		return c, nil
	}
	if err := SpawnBroker(); err != nil {
		return nil, fmt.Errorf("start broker: %w", err)
	}
	c, err := WaitDial(8 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("broker not reachable at %s: %w", ipc.Path(), err)
	}
	return c, nil
}

// WaitDial retries Dial until it succeeds or wait passes (at least one attempt).
func WaitDial(wait time.Duration) (*Conn, error) {
	deadline := time.Now().Add(wait)
	for {
		c, err := Dial()
		if err == nil || !time.Now().Before(deadline) {
			return c, err
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.done)
		c.nc.Close()
	})
}

func (c *Conn) readLoop() {
	defer c.Close()
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var f proto.Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			continue
		}
		if f.Op == "event" {
			var ev proto.Event
			if json.Unmarshal(f.Data, &ev) != nil {
				continue
			}
			select {
			case c.Events <- ev:
			case <-c.done:
				return
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- f
		}
	}
}

// Call sends op and waits for the reply; broker errors come back as *proto.Error.
func (c *Conn) Call(op string, args, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan proto.Frame, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	f := proto.Frame{Op: op, ID: id}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		f.Args = raw
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	_, err = c.nc.Write(b)
	c.wmu.Unlock()
	if err != nil {
		c.Close()
		return ErrClosed
	}

	select {
	case r := <-ch:
		if r.Op == "err" {
			return &proto.Error{Code: r.Code, Msg: r.Msg}
		}
		if out != nil && len(r.Data) > 0 {
			return json.Unmarshal(r.Data, out)
		}
		return nil
	case <-c.done:
		return ErrClosed
	case <-time.After(callTimeout):
		return fmt.Errorf("broker: %s timed out", op)
	}
}

// SpawnBroker starts "<self> broker" detached; output goes to <cache>/ccqc/broker.log.
func SpawnBroker() error {
	exe, err := installedExe()
	if err != nil {
		return err
	}
	var logf *os.File
	if dir, derr := os.UserCacheDir(); derr == nil {
		dir = filepath.Join(dir, "ccqc")
		if os.MkdirAll(dir, 0o700) == nil {
			logf, _ = os.OpenFile(filepath.Join(dir, "broker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		}
	}
	if logf != nil {
		defer logf.Close()
	}
	modes := []bool{false}
	if canBreakaway {
		modes = []bool{true, false} // breakaway may be denied by the job object
	}
	for _, breakaway := range modes {
		cmd := exec.Command(exe, "broker")
		if logf != nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		detach(cmd, breakaway)
		if err = cmd.Start(); err == nil {
			return cmd.Process.Release()
		}
	}
	return err
}

// installedExe: the path this binary is installed under. After an update while running,
// the process may still point at its old file: "ccqc.exe.old" (renamed by the Windows
// installer) or "ccqc (deleted)" (replaced on Linux) — the broker must start from the new one.
func installedExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	if trimmed := strings.TrimSuffix(exe, ".old"); trimmed != exe {
		if _, err := os.Stat(trimmed); err == nil {
			exe = trimmed
		}
	}
	return exe, nil
}
