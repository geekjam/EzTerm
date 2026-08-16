package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
)

// knownFlag reports whether name is a flag the given FlagSet defines.
func knownFlag(fs *flag.FlagSet, name string) bool {
	flagName := name
	for len(flagName) > 0 && flagName[0] == '-' {
		flagName = flagName[1:]
	}
	found := false
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == flagName {
			found = true
		}
	})
	return found
}

// reorderPositionals moves positional (non-flag) arguments to the end so that
// flags may appear before or after them, e.g. `send <id> --text x`.
func reorderPositionals(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	var reordered []string
	seenDoubleDash := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case seenDoubleDash:
			positionals = append(positionals, arg)
		case arg == "--":
			seenDoubleDash = true
			reordered = append(reordered, arg)
		case len(arg) > 1 && arg[0] == '-':
			// A self-contained value (--flag=value) needs no consumption. A bare
			// flag that takes a value consumes the next argument, even if that
			// argument looks like a flag (e.g. `config local --command sh --args -c`).
			reordered = append(reordered, arg)
			if !strings.Contains(arg, "=") && knownFlag(fs, arg) && flagTakesValue(fs, arg) && i+1 < len(args) {
				reordered = append(reordered, args[i+1])
				i++
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	return append(reordered, positionals...)
}

// isFlagValue reports whether an argument looks like a flag with an inline value
// such as --timeout=5 or -x=1.
func isFlagValue(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' {
		return false
	}
	for _, ch := range arg[1:] {
		if ch == '=' {
			return true
		}
	}
	return false
}

// flagTakesValue reports whether the flag consumes a separate value argument.
// Bool flags implement IsBoolFlag and never consume a separate value.
func flagTakesValue(fs *flag.FlagSet, name string) bool {
	flagName := name
	for len(flagName) > 0 && flagName[0] == '-' {
		flagName = flagName[1:]
	}
	f := fs.Lookup(flagName)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

// parseCommandArgs parses subcommand flags after moving positionals to the end.
// It returns the parsed FlagSet and the positional arguments.
func parseCommandArgs(fs *flag.FlagSet, args []string) []string {
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderPositionals(fs, args)); err != nil {
		return nil
	}
	positionals := fs.Args()
	if positionals == nil {
		return []string{}
	}
	return positionals
}

func cmdStart(c *client, jsonOut bool, dataDir string, args []string) int {
	fs := flag.NewFlagSet("ezterm start", flag.ContinueOnError)
	name := fs.String("name", "", "saved config name to start")
	web := fs.Bool("web", false, "enable the Web terminal page (PTY mode only)")
	rows := fs.Int("rows", 24, "initial PTY rows")
	cols := fs.Int("cols", 80, "initial PTY cols")
	dialTimeout := fs.Int("timeout", 15, "dial timeout in seconds for remote sessions")
	if parseCommandArgs(fs, args) == nil {
		return 2
	}
	if fs.NArg() > 0 {
		printError(jsonOut, "unexpected arguments: %v", fs.Args())
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		printError(jsonOut, "--name is required (the saved config to start)")
		return 2
	}

	store := configstore.NewStore(dataDir)
	resolved, err := store.Resolve(*name)
	if err != nil {
		printError(jsonOut, "start session: %v", err)
		return 2
	}
	req := api.CreateSessionRequest{
		Rows:               *rows,
		Cols:               *cols,
		Web:                *web,
		DialTimeoutSeconds: *dialTimeout,
	}
	switch resolved.Type {
	case configstore.TypeLocal:
		req.Command = resolved.Local.Command
		req.Args = resolved.Local.Args
		req.Mode = resolved.Local.Mode
	case configstore.TypeSSH:
		req.SSHConfig = *name
		req.Mode = api.ModePTY
	}

	session, err := c.create(req, time.Duration(*dialTimeout+10)*time.Second)
	if err != nil {
		printError(jsonOut, "start session: %v", err)
		return exitCodeFor(err)
	}
	code := printSession(jsonOut, session, "created")
	if *web && session.WebURL != "" && !jsonOut {
		fmt.Printf("web terminal: %s\n", session.WebURL)
	}
	return code
}

func cmdSend(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm send", flag.ContinueOnError)
	text := fs.String("text", "", "text to send")
	pressEnter := fs.Bool("press-enter", false, "append a newline to the input")
	pressKey := fs.String("press-key", "", "send a key or key combination (e.g. ctrl+c, enter, f5, ctrl+shift+up)")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	var pressKeySet, textSet, pressEnterSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "press-key":
			pressKeySet = true
		case "text":
			textSet = true
		case "press-enter":
			pressEnterSet = true
		}
	})
	if pressKeySet && (textSet || pressEnterSet) {
		printError(jsonOut, "--press-key is mutually exclusive with --text and --press-enter")
		return 2
	}
	if pressKeySet {
		key, err := parseKey(*pressKey)
		if err != nil {
			printError(jsonOut, "press-key: %v", err)
			return 2
		}
		if err := c.sendRawInput(id, key); err != nil {
			printError(jsonOut, "send input: %v", err)
			return exitCodeFor(err)
		}
	} else if err := c.send(id, *text, *pressEnter); err != nil {
		printError(jsonOut, "send input: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"ok": true, "session_id": id})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("input sent to session %s\n", id)
	}
	return 0
}

func cmdRead(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm read", flag.ContinueOnError)
	readerID := fs.Int("reader", 0, "reader ID (0 = default cursor)")
	timeoutSec := fs.Float64("timeout", 30, "block up to this many seconds for output (0 = non-blocking)")
	raw := fs.Bool("raw", false, "keep ANSI escape sequences in the output")
	maxBytes := fs.Int("max-bytes", 0, "limit returned bytes (0 = unlimited)")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}

	timeout := time.Duration(*timeoutSec * float64(time.Second))
	if *timeoutSec == 0 {
		timeout = 0
	}
	response, err := c.read(id, *readerID, timeout, *raw, *maxBytes)
	if err != nil {
		printError(jsonOut, "read output: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		out, _ := json.Marshal(response)
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Print(response.Data)
	}
	return 0
}

func cmdAttach(c *client, jsonOut bool, args []string) int {
	if jsonOut {
		printError(true, "attach is an interactive command and cannot be combined with --json")
		return 2
	}
	fs := flag.NewFlagSet("ezterm attach", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(false, "%v", err)
		return 2
	}
	if len(positionals) > 1 {
		printError(false, "unexpected arguments: %v", positionals[1:])
		return 2
	}
	return runAttach(c, id)
}

func cmdTerminate(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm terminate", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	session, err := c.terminate(id)
	if err != nil {
		printError(jsonOut, "terminate session: %v", err)
		return exitCodeFor(err)
	}
	return printSession(jsonOut, session, "terminated")
}

func cmdDelete(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm delete", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	if err := c.delete(id); err != nil {
		printError(jsonOut, "delete session: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"deleted": true, "session_id": id})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("session %s deleted\n", id)
	}
	return 0
}

func cmdList(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm list", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	sessions, err := c.list()
	if err != nil {
		printError(jsonOut, "list sessions: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"sessions": sessions})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		printSessionTable(sessions)
	}
	return 0
}

func cmdHealth(jsonOut bool, port int, args []string) int {
	if newClient(port).checkHealth() {
		if jsonOut {
			fmt.Fprintln(os.Stdout, `{"status": "ok"}`)
		} else {
			fmt.Println("ok")
		}
		return 0
	}
	printError(jsonOut, "daemon not reachable on port %d", port)
	return 2
}

// printSession renders a single session result.
func printSession(jsonOut bool, s api.Session, action string) int {
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"session": s})
		fmt.Fprintln(os.Stdout, string(out))
		return 0
	}
	fmt.Printf("session %s %s\n", s.ID, action)
	printSessionTable([]api.Session{s})
	return 0
}

func printSessionTable(sessions []api.Session) {
	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}
	const format = "%-12s  %-24s  %-6s  %-10s  %-10s  %-6s  %s\n"
	fmt.Printf(format, "ID", "NAME", "MODE", "STATUS", "SSH", "EXIT", "COMMAND")
	for _, s := range sessions {
		ssh := s.SSHConfig
		if ssh == "" {
			ssh = "internal"
		}
		exit := ""
		if s.Status == api.StatusExited || s.Status == api.StatusTerminated {
			exit = fmt.Sprintf("%d", s.ExitCode)
		}
		command := s.Command
		if command == "" {
			command = "(default shell)"
		}
		if len(s.Args) > 0 {
			command = strings.TrimSpace(command + " " + strings.Join(s.Args, " "))
		}
		fmt.Printf(format, s.ID, s.Name, s.Mode, s.Status, ssh, exit, command)
	}
}

// exitCodeFor maps client errors to the documented exit codes.
func exitCodeFor(err error) int {
	if errors.Is(err, errSessionNotFound) {
		return 1
	}
	return 2
}
