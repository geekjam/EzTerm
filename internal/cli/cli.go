package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"ezterm/internal/daemon"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Run executes the ezterm CLI and returns a process exit code.
func Run(args []string) int {
	globals, rest := splitGlobalFlags(args)

	globalFS := flag.NewFlagSet("ezterm", flag.ContinueOnError)
	port := globalFS.Int("port", 18766, "daemon HTTP port")
	dataDir := globalFS.String("data-dir", "", "daemon data directory (default ~/.ezterm)")
	jsonOut := globalFS.Bool("json", false, "emit stable JSON output")
	logLevel := globalFS.String("log-level", "info", "daemon log level when auto-spawned (debug, info, warn, error)")
	globalFS.SetOutput(os.Stderr)
	if err := globalFS.Parse(globals); err != nil {
		return 2
	}

	if len(rest) == 0 {
		usage(globalFS, "")
		return 2
	}
	cmd := rest[0]
	cmdArgs := rest[1:]

	// Expand the data directory once for all commands.
	expandedDataDir, err := expandDataDir(*dataDir)
	if err != nil {
		printError(*jsonOut, "%s", err.Error())
		return 2
	}

	// The daemon subcommand runs in-process and never talks to another daemon.
	if cmd == "daemon" {
		return daemon.Run(append([]string{"--port", strconv.Itoa(*port), "--data-dir", expandedDataDir, "--log-level", *logLevel}, cmdArgs...))
	}
	if cmd == "version" {
		fmt.Fprintln(os.Stdout, "ezterm "+Version)
		return 0
	}

	switch cmd {
	case "config":
		return cmdConfig(*jsonOut, expandedDataDir, cmdArgs)
	case "health":
		return cmdHealth(*jsonOut, *port, cmdArgs)
	case "start", "send", "read", "attach", "terminate", "delete", "list":
		client := newClient(*port)
		if !client.checkHealth() {
			if err := ensureDaemon(*port, expandedDataDir, *logLevel); err != nil {
				printError(*jsonOut, "cannot reach ezterm daemon on 127.0.0.1:%d: %v", *port, err)
				return 2
			}
		}
		switch cmd {
		case "start":
			return cmdStart(client, *jsonOut, expandedDataDir, cmdArgs)
		case "send":
			return cmdSend(client, *jsonOut, cmdArgs)
		case "read":
			return cmdRead(client, *jsonOut, cmdArgs)
		case "attach":
			return cmdAttach(client, *jsonOut, cmdArgs)
		case "terminate":
			return cmdTerminate(client, *jsonOut, cmdArgs)
		case "delete":
			return cmdDelete(client, *jsonOut, cmdArgs)
		default:
			return cmdList(client, *jsonOut, cmdArgs)
		}
	default:
		usage(globalFS, fmt.Sprintf("unknown command %q", cmd))
		return 2
	}
}

// splitGlobalFlags extracts global flags from any position so they can be used
// before or after the subcommand.
// splitGlobalFlags separates terminal-global flags from the subcommand and its
// flags. Global flags are only recognized before the command or after any
// daemon-requiring command; subcommands that own their own flags (config,
// daemon) keep them.
func splitGlobalFlags(args []string) (globals, rest []string) {
	command := ""
	configSubcommand := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		preserveConfigPort := command == "config" && configSubcommand == "ssh" && isPortFlag(a)
		if command != "daemon" && !preserveConfigPort && takeGlobalFlag(args, &i, &globals) {
			continue
		}
		rest = append(rest, a)
		if command == "" && !strings.HasPrefix(a, "-") {
			command = a
		} else if command == "config" && configSubcommand == "" && !strings.HasPrefix(a, "-") {
			configSubcommand = a
		}
	}
	return globals, rest
}

// isPortFlag identifies --port forms. The config ssh subcommand owns this
// flag for the remote SSH port; all other commands treat it as global.
func isPortFlag(arg string) bool {
	return arg == "--port" || strings.HasPrefix(arg, "--port=")
}

// takeGlobalFlag reports whether args[i] is a terminal-global flag, appending it
// (and its value, if any) to globals and advancing i past the value.
func takeGlobalFlag(args []string, i *int, globals *[]string) bool {
	a := args[*i]
	switch {
	case a == "--json" || a == "--help" || a == "-h":
		*globals = append(*globals, a)
		return true
	case a == "--port" || a == "--data-dir" || a == "--log-level":
		*globals = append(*globals, a)
		if *i+1 < len(args) {
			*i++
			*globals = append(*globals, args[*i])
		}
		return true
	case strings.HasPrefix(a, "--port=") || strings.HasPrefix(a, "--data-dir=") || strings.HasPrefix(a, "--log-level="):
		*globals = append(*globals, a)
		return true
	}
	return false
}

// stringList is a repeatable command-line flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func usage(fs *flag.FlagSet, msg string) {
	if msg != "" {
		fmt.Fprintln(os.Stderr, "ezterm: "+msg)
	}
	fmt.Fprint(os.Stderr, `Usage: ezterm [global flags] <command> [flags]

Commands:
  start        start a session from a saved config (--name <config>; optional --web/--rows/--cols/--timeout)
  send         write input to a session (--text/--press-enter, or --press-key KEY)
  read         read output from a session
  attach       attach to a running PTY session (Ctrl+] to detach)
  terminate    stop a running session
  delete       remove a finished session
  list         list sessions
  config       manage launch configs (local, ssh, list, delete)
  daemon       run the daemon in the foreground
  health       probe the daemon
  version      print the version

Global flags:
  --port <n>       daemon HTTP port (default 18766)
  --data-dir <dir> daemon data directory (default ~/.ezterm)
  --json           emit stable JSON output
  --log-level <l>  daemon log level when auto-spawned (debug, info, warn, error)
`)
}

// printError renders an error according to the output mode.
func printError(jsonOut bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if jsonOut {
		fmt.Fprintln(os.Stdout, `{"error": `+strconv.Quote(msg)+`}`)
	} else {
		fmt.Fprintln(os.Stderr, "ezterm: "+msg)
	}
}
