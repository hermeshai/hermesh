package herhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHer listens on a socket like her: each connection answers initialize,
// then hands every other request to handle with the connection to write to.
func fakeHer(t *testing.T, handle func(req map[string]any, c net.Conn)) string {
	dir, err := os.MkdirTemp("", "hh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "her.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				sc.Buffer(nil, 1<<20)
				for sc.Scan() {
					var req map[string]any
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						t.Errorf("not one JSON message per line: %q", sc.Text())
						return
					}
					if req["method"] == "initialize" {
						fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"result":{"protocol_version":2,"server_info":{"name":"her","version":"9.9"},"capabilities":{}}}`+"\n", req["id"])
						continue
					}
					handle(req, c)
				}
			}()
		}
	}()
	return sock
}

// Answers find their requests by id, in whatever order her sends them;
// notifications reach the reader in order; her's errors come back as her
// sent them; the version is initialize's.
func TestConnCalls(t *testing.T) {
	var mu sync.Mutex
	var slow any
	sock := fakeHer(t, func(req map[string]any, c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "slow":
			slow = req["id"]
		case "fast":
			fmt.Fprintf(c, `{"jsonrpc":"2.0","method":"note","params":{"n":1}}`+"\n")
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"result":{"echo":%s}}`+"\n", req["id"], mustJSON(t, req["params"]))
			fmt.Fprintf(c, `{"jsonrpc":"2.0","method":"note","params":{"n":2}}`+"\n")
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"result":"slow"}`+"\n", slow)
		case "bad":
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32040,"message":"no such bot","data":{"kind":"bot_not_found"}}}`+"\n", req["id"])
		}
	})
	ctx := context.Background()
	c, err := dial(ctx, sock, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.version != "9.9" {
		t.Fatalf("version %q", c.version)
	}
	slowDone := make(chan string)
	go func() {
		res, err := c.call(ctx, "slow", nil)
		if err != nil {
			t.Error(err)
		}
		slowDone <- string(res.GetResult())
	}()
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return slow != nil })
	// Params on several lines still go as one.
	res, err := c.call(ctx, "fast", json.RawMessage("{\n  \"a\": [1,\n 2]\n}"))
	if err != nil || string(res.Result) != `{"echo":{"a":[1,2]}}` {
		t.Fatalf("fast: %s, %v", res.GetResult(), err)
	}
	if got := <-slowDone; got != `"slow"` {
		t.Fatalf("slow: %s", got)
	}
	for _, want := range []string{`{"n":1}`, `{"n":2}`} {
		n := <-c.notes
		if n.method != "note" || string(n.params) != want || !strings.Contains(string(n.line), `"method":"note"`) {
			t.Fatalf("note %s %s %s", n.method, n.params, n.line)
		}
	}
	res, err = c.call(ctx, "bad", nil)
	if err != nil || res.Error.GetCode() != -32040 || res.Error.GetMessage() != "no such bot" || string(res.Error.GetData()) != `{"kind":"bot_not_found"}` {
		t.Fatalf("bad: %v, %v", res, err)
	}
}

// A connection her closes fails what waits on it and what comes after; one
// whose notifications nobody reads is dropped rather than kept growing.
func TestConnEnds(t *testing.T) {
	sock := fakeHer(t, func(req map[string]any, c net.Conn) {
		switch req["method"] {
		case "die":
			c.Close()
		case "flood":
			w := bufio.NewWriter(c)
			for i := range queuedNotes + 10 {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","method":"note","params":{"n":%d}}`+"\n", i)
			}
			w.Flush()
		}
	})
	ctx := context.Background()
	c, err := dial(ctx, sock, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(ctx, "die", nil); err == nil {
		t.Fatal("a call on a closed connection answered")
	}
	if _, err := c.call(ctx, "anything", nil); err == nil {
		t.Fatal("a call after the connection closed answered")
	}

	c, err = dial(ctx, sock, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.call(cctx, "flood", nil); err == nil || !strings.Contains(err.Error(), "not read in time") {
		t.Fatalf("flooded: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
