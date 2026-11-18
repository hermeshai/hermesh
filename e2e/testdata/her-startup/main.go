// A Her fixture that can stop answering after initialize.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	switch os.Args[1] {
	case "--version":
		fmt.Println("test-her")
	case "serve":
		socket := strings.TrimPrefix(os.Args[3], "unix://")
		if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
			panic(err)
		}
		os.Remove(socket)
		ln, err := net.Listen("unix", socket)
		if err != nil {
			panic(err)
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				panic(err)
			}
			go serve(conn)
		}
	}
}

func serve(conn net.Conn) {
	defer conn.Close()
	d, e := json.NewDecoder(conn), json.NewEncoder(conn)
	for {
		var req struct {
			ID     int64
			Method string
		}
		if d.Decode(&req) != nil {
			return
		}
		result := json.RawMessage("{}")
		switch req.Method {
		case "initialize":
			result = json.RawMessage(`{"server_info":{"version":"test-her"},"methods":["bots/subscribe"]}`)
		case "bots/subscribe":
			home := os.Getenv("HER_HOME")
			if home == "" {
				userHome, _ := os.UserHomeDir()
				home = filepath.Join(userHome, ".her")
			}
			if _, err := os.Stat(filepath.Join(home, "stall-startup")); err == nil {
				continue
			}
			result = json.RawMessage(`{"bots":[]}`)
		}
		if e.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			return
		}
	}
}
