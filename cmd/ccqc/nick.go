package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// runNick: ccqc nick reset <token|-> <nick>.
func runNick(args []string) error {
	if len(args) != 3 || args[0] != "reset" {
		return errors.New("usage: ccqc nick reset - <nick>   (the token is read from stdin: " +
			"a token in the arguments shows up in process lists and shell history)")
	}
	token, name := strings.TrimSpace(args[1]), strings.TrimPrefix(args[2], "@")
	if token == "-" {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		token = strings.TrimSpace(line)
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	// With an owner registered this is an administrator operation, signed with the owner key.
	if info, ierr := brokerOwner(c); ierr == nil && info.Registered {
		_, id, perr := proto.ParseToken(token)
		if perr != nil {
			return perr
		}
		text, err := adminDo(c, "nick_reset", proto.AdminRole{Chat: id, Nick: name})
		if err != nil {
			return err
		}
		fmt.Println(text)
		return nil
	}
	if err := c.Call("nick_reset", proto.NickResetArgs{Token: token, Name: name}, nil); err != nil {
		if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
			return errors.New("the running broker is older and can't reset nicks: run `ccqc broker restart` first")
		}
		return err
	}
	fmt.Printf("nick @%s is free: its history and rooms stay; the next join under it (chat_join or /quant-join) binds a new key\n", name)
	return nil
}
