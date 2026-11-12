package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/broker"
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// exitNotRunning: `ccqc broker status` exit code when no broker runs (for scripts).
const exitNotRunning = 3

// runBroker: ccqc broker [run|status|stop|restart].
func runBroker(args []string) error {
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "run":
		return broker.Run()
	case "status":
		info, err := brokerInfo()
		if err != nil {
			fmt.Println("broker: not running")
			os.Exit(exitNotRunning)
		}
		printInfo(info)
		return nil
	case "stop":
		return stopBroker()
	case "restart":
		if err := stopBroker(); err != nil {
			return err
		}
		if err := client.SpawnBroker(); err != nil {
			return fmt.Errorf("start broker: %w", err)
		}
		c, err := client.WaitDial(8 * time.Second)
		if err != nil {
			return fmt.Errorf("broker did not come up at %s: %w (see broker.log)", ipc.Path(), err)
		}
		c.Close()
		info, err := brokerInfo()
		if err != nil {
			return err
		}
		fmt.Print("started: ")
		printInfo(info)
		return nil
	}
	return errors.New("usage: ccqc broker [run|status|stop|restart]")
}

func brokerInfo() (proto.BrokerInfo, error) {
	var info proto.BrokerInfo
	c, err := client.Dial()
	if err != nil {
		return info, err
	}
	defer c.Close()
	err = c.Call("ping", nil, &info)
	return info, err
}

func printInfo(i proto.BrokerInfo) {
	up := ""
	if t, err := time.Parse(time.RFC3339, i.Started); err == nil {
		up = time.Since(t).Round(time.Second).String()
	}
	fmt.Printf("broker %s, pid %d, up %s, %d chat(s) loaded, %d member(s) connected\n  store: %s\n  pipe:  %s\n",
		i.Version, i.PID, up, i.Chats, i.Clients, i.Store, ipc.Path())
	if len(i.ByProto) > 0 {
		levels := make([]string, 0, len(i.ByProto))
		for l := range i.ByProto {
			levels = append(levels, l)
		}
		sort.Strings(levels)
		parts := make([]string, len(levels))
		for k, l := range levels {
			parts[k] = fmt.Sprintf("proto %s: %d", l, i.ByProto[l])
		}
		fmt.Println("  clients:", strings.Join(parts, "; "))
	}
	if i.Node != "" {
		fmt.Println("  node:  ", i.Node)
	}
	if i.Owner != "" {
		fmt.Printf("  owner:  %s (registered %s)\n", i.Owner, i.OwnerSince)
	} else {
		fmt.Println("  owner:  not initialized: run `ccqc owner init` before agents run")
	}
	if i.Warning != "" {
		fmt.Println("  warning:", i.Warning)
	}
}

// stopBroker asks the broker to stop and waits until it is gone. Connected sessions
// reconnect by themselves (to the next broker) and lose nothing: state is in the database.
func stopBroker() error {
	c, err := client.Dial()
	if err != nil {
		fmt.Println("broker: not running")
		return nil
	}
	var info proto.BrokerInfo
	_ = c.Call("ping", nil, &info)
	err = c.Call("shutdown", nil, nil)
	c.Close()
	if err != nil {
		return fmt.Errorf("stop broker: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := client.Dial()
		if err != nil {
			// The endpoint is gone, but the process still closes the database (and on Unix holds
			// the lock file); the next broker can't take over before it exits.
			if info.PID == 0 || !pidAlive(info.PID) {
				fmt.Printf("broker pid %d stopped\n", info.PID)
				return nil
			}
		} else {
			probe.Close()
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("broker pid %d did not stop within 10s", info.PID)
}
