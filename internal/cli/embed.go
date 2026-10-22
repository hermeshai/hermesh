package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Identity is a client's credentials kept by the caller rather than in
// files: the MCP gateway runs commands for the connections it holds keys for.
type Identity struct {
	RelayURL  string
	OwnerID   string
	Key       *identity.SoftwareKey
	RootChain *identityv1.RootChain
	// Grant is nil while the owner has not approved the client yet.
	Grant *identityv1.Signed
}

// ExecOptions run a command for an Identity.
type ExecOptions struct {
	Identity   Identity
	HTTPClient *http.Client
	// Adopted is called with the credentials a command picked up from the
	// relay (a renewed or changed grant, a longer root chain).
	Adopted func(rootChain *identityv1.RootChain, grant *identityv1.Signed)
}

// Exec runs one command (args without "hermesh", e.g. "session", "get",
// "s_..."), for opts.Identity, with JSON output: it returns the command's
// document (docs/cli-output.md) and its exit code.
func Exec(ctx context.Context, opts ExecOptions, args []string) ([]byte, int) {
	a := &app{hc: opts.HTTPClient, mem: &opts}
	if a.hc == nil {
		a.hc = http.DefaultClient
	}
	root := a.root("json")
	var stdout bytes.Buffer
	// JSON output, as a flag before any "--" that ends them.
	args = slices.Clone(args)
	at := slices.Index(args, "--")
	if at < 0 {
		at = len(args)
	}
	root.SetArgs(slices.Insert(args, at, "--output=json"))
	root.SetOut(&stdout)
	root.SetErr(io.Discard)
	root.SetIn(bytes.NewReader(nil))
	code := a.execute(ctx, root, &stdout, io.Discard)
	return stdout.Bytes(), code
}

func (a *app) loadMem() error {
	id := a.mem.Identity
	if id.Grant == nil {
		return errNotLoggedIn
	}
	a.st = state{RelayURL: id.RelayURL, OwnerID: id.OwnerID}
	a.key = id.Key
	a.trust = identity.NewTrust(id.OwnerID)
	if _, err := a.trust.AcceptRootChain(id.RootChain); err != nil {
		return fmt.Errorf("owner root chain: %w", err)
	}
	a.grant = id.Grant
	a.g = &identityv1.Grant{}
	return identity.Unverified(a.grant, a.g)
}

// Tool is a CLI command as a tool for an agent (the MCP gateway's tools):
// the same command, flags and output contract.
type Tool struct {
	// Name is the command path joined by "_" ("session_start").
	Name string
	// Path is the command path ("session", "start").
	Path        []string
	Description string
	// Arg is the positional argument (e.g. session_id), if any.
	Arg    *ToolParam
	Params []ToolParam
}

// ToolParam is one flag (or the positional argument) of a Tool.
type ToolParam struct {
	// Name is the flag's name with "_" for "-" ("question_timeout").
	Name string
	Flag string
	// Type: "string", "boolean", "integer", "duration" (a Go duration such
	// as "30s" or "5m"), or "array" (of strings; a repeatable flag).
	Type     string
	Help     string
	Required bool
	Default  string
}

// toolCommands are the commands an agent reaches through tools: the
// pairing commands (login, logout) and version are the local CLI's own.
var toolCommands = [][]string{
	{"auth", "status"},
	{"project", "list"},
	{"session", "start"}, {"session", "get"}, {"session", "list"}, {"session", "events"},
	{"session", "send"}, {"session", "answer"}, {"session", "interrupt"},
	{"session", "archive"}, {"session", "unarchive"}, {"session", "diff"},
}

// localFlags read this computer's files or stdin, which a remote caller
// cannot use.
var localFlags = map[string]bool{"file": true, "prompt-file": true, "message-file": true, "output": true, "help": true}

// Tools lists the CLI commands an agent can run through Exec, as tools.
func Tools() []Tool {
	root := (&app{}).root("json")
	var out []Tool
	for _, path := range toolCommands {
		cmd, _, err := root.Find(path)
		if err != nil || cmd == root {
			panic(fmt.Sprintf("cli: no command %v", path))
		}
		t := Tool{Name: strings.Join(path, "_"), Path: path, Description: describe(cmd)}
		if _, arg, ok := strings.Cut(cmd.Use, " "); ok {
			name := strings.ReplaceAll(strings.Trim(arg, "<>[]"), " ", "_")
			t.Arg = &ToolParam{Name: name, Type: "string", Required: strings.HasPrefix(arg, "<"), Help: "the " + strings.Trim(arg, "<>[]")}
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if localFlags[f.Name] || f.Hidden {
				return
			}
			p := ToolParam{Name: strings.ReplaceAll(f.Name, "-", "_"), Flag: f.Name, Help: f.Usage, Default: f.DefValue,
				Required: slices.Contains(f.Annotations[cobra.BashCompOneRequiredFlag], "true")}
			switch f.Value.Type() {
			case "bool":
				p.Type = "boolean"
			case "duration":
				p.Type = "duration"
			case "stringArray", "stringSlice":
				p.Type, p.Default = "array", ""
			case "int", "int32", "int64", "uint", "uint32", "uint64":
				p.Type = "integer"
			default:
				p.Type = "string"
			}
			t.Params = append(t.Params, p)
		})
		out = append(out, t)
	}
	return out
}

func describe(cmd *cobra.Command) string {
	if cmd.Long != "" {
		return cmd.Short + ".\n\n" + cmd.Long
	}
	return cmd.Short
}

// Args are the command-line arguments that run t with the values given
// (by parameter name; strings, booleans, numbers or lists of strings).
func (t Tool) Args(values map[string]any) ([]string, error) {
	args := slices.Clone(t.Path)
	for _, p := range t.Params {
		v, ok := values[p.Name]
		if !ok || v == nil {
			if p.Required {
				return nil, fmt.Errorf("%s is required", p.Name)
			}
			continue
		}
		switch p.Type {
		case "array":
			list, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: a list of strings", p.Name)
			}
			for _, x := range list {
				args = append(args, fmt.Sprintf("--%s=%v", p.Flag, x))
			}
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%s: true or false", p.Name)
			}
			args = append(args, fmt.Sprintf("--%s=%t", p.Flag, b))
		case "integer":
			n, ok := v.(float64)
			if !ok || n != float64(int64(n)) || n < 0 {
				return nil, fmt.Errorf("%s: a whole number", p.Name)
			}
			args = append(args, fmt.Sprintf("--%s=%d", p.Flag, int64(n)))
		default:
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s: a string", p.Name)
			}
			args = append(args, fmt.Sprintf("--%s=%s", p.Flag, s))
		}
	}
	if t.Arg != nil {
		v, _ := values[t.Arg.Name].(string)
		if v == "" {
			return nil, fmt.Errorf("%s is required", t.Arg.Name)
		}
		args = append(args, "--", v)
	}
	return args, nil
}
