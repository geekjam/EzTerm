package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"ezterm/internal/api"
	"ezterm/internal/sshconfig"
)

// cmdSSHConfig manages SSH profiles directly on disk (no daemon required).
func cmdSSHConfig(jsonOut bool, dataDir string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ezterm ssh-config <init|list> [flags]")
		return 2
	}
	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "init":
		return sshConfigInit(jsonOut, dataDir, subArgs)
	case "list":
		return sshConfigList(jsonOut, dataDir, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "ezterm: unknown ssh-config subcommand %q\n", sub)
		return 2
	}
}

func sshConfigInit(jsonOut bool, dataDir string, args []string) int {
	fs := flag.NewFlagSet("ezterm ssh-config init", flag.ContinueOnError)
	host := fs.String("host", "", "remote host")
	port := fs.Int("port", 22, "SSH port")
	user := fs.String("user", "", "login user")
	auth := fs.String("auth", "password", "auth method: password or key")
	password := fs.String("password", "", "password (auth=password)")
	keyPath := fs.String("key-path", "", "private key path (auth=key)")
	shell := fs.String("shell", "", "optional default remote shell")
	fs.SetOutput(os.Stderr)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 1 {
		printError(jsonOut, "usage: ezterm ssh-config init [flags] <name> (flags: --host, --user, --auth password|key, --password, --key-path, --shell, --port)")
		return 2
	}
	name := positionals[0]

	store := sshconfig.NewStore(dataDir)
	if store.Exists(name) {
		printError(jsonOut, "SSH profile %q already exists", name)
		return 2
	}
	profile := &sshconfig.Profile{
		Host:         firstNonEmpty(*host, "change-me.example.com"),
		Port:         *port,
		User:         firstNonEmpty(*user, "change-me"),
		AuthMethod:   sshconfig.AuthMethod(*auth),
		Password:     *password,
		KeyPath:      *keyPath,
		DefaultShell: *shell,
	}
	if profile.AuthMethod == sshconfig.AuthPassword && profile.Password == "" {
		profile.Password = "change-me"
	}
	if err := store.Save(name, profile); err != nil {
		printError(jsonOut, "save SSH config: %v", err)
		return 2
	}

	path := store.ProfilePath(name)
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"name": name, "path": path})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("SSH profile %q created at %s\n", name, path)
		fmt.Println("Edit the file to set host/user/credentials, then: ezterm start --ssh-config " + name)
	}
	return 0
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sshConfigList(jsonOut bool, dataDir string, args []string) int {
	fs := flag.NewFlagSet("ezterm ssh-config list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	store := sshconfig.NewStore(dataDir)
	profiles, err := store.List()
	if err != nil {
		printError(jsonOut, "list SSH configs: %v", err)
		return 2
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"ssh_configs": profiles})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		printSSHTable(profiles)
	}
	return 0
}

func printSSHTable(profiles []api.SSHProfileSummary) {
	if len(profiles) == 0 {
		fmt.Println("no SSH profiles")
		return
	}
	const format = "%-16s  %-32s  %-8s  %-10s  %-10s\n"
	fmt.Printf(format, "NAME", "HOST", "PORT", "USER", "AUTH")
	for _, p := range profiles {
		fmt.Printf(format, p.Name, p.Host, fmt.Sprintf("%d", p.Port), p.User, p.AuthMethod)
	}
}

// expandDataDir resolves the CLI's data directory.
func expandDataDir(path string) (string, error) {
	if path == "" {
		return defaultDataDir()
	}
	return expandTilde(path)
}
