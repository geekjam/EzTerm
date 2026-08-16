package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/sshconfig"
)

// cmdConfig manages local and SSH launch configs directly on disk. It does not
// require a daemon because config files are client-owned state.
func cmdConfig(jsonOut bool, dataDir string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ezterm config <local|ssh|list|delete> [flags]")
		return 2
	}

	store := configstore.NewStore(dataDir)
	switch args[0] {
	case "local":
		return configLocal(jsonOut, store, args[1:])
	case "ssh":
		return configSSH(jsonOut, store, args[1:])
	case "list":
		return configList(jsonOut, store, args[1:])
	case "delete":
		return configDelete(jsonOut, store, args[1:])
	default:
		printError(jsonOut, "unknown config subcommand %q", args[0])
		return 2
	}
}

func configLocal(jsonOut bool, store *configstore.Store, args []string) int {
	fs := flag.NewFlagSet("ezterm config local", flag.ContinueOnError)
	name := fs.String("name", "", "config name")
	command := fs.String("command", "", "command to run (empty = default shell)")
	var cmdArgs stringList
	fs.Var(&cmdArgs, "args", "command argument (repeatable; a value may begin with '-')")
	mode := fs.String("mode", string(api.ModePTY), "session mode: pty or pipe")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 0 {
		printError(jsonOut, "unexpected arguments: %v", positionals)
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		printError(jsonOut, "--name is required")
		return 2
	}
	cfg := &configstore.LocalConfig{
		Command: *command,
		Args:    append([]string(nil), cmdArgs...),
		Mode:    api.Mode(*mode),
	}
	if cfg.Mode == api.ModePipe && strings.TrimSpace(cfg.Command) == "" {
		printError(jsonOut, "--command is required for a pipe config")
		return 2
	}
	if err := store.SaveLocal(*name, cfg); err != nil {
		printError(jsonOut, "save local config: %v", err)
		return 2
	}
	return printConfigSaved(jsonOut, *name, configstore.TypeLocal)
}

func configSSH(jsonOut bool, store *configstore.Store, args []string) int {
	fs := flag.NewFlagSet("ezterm config ssh", flag.ContinueOnError)
	name := fs.String("name", "", "config name")
	host := fs.String("host", "", "remote host")
	port := fs.Int("port", sshconfig.DefaultPort, "SSH port")
	user := fs.String("user", "", "login user")
	auth := fs.String("auth", "", "auth method: password or key")
	password := fs.String("password", "", "password (auth=password)")
	keyPath := fs.String("key-path", "", "private key path (auth=key)")
	shell := fs.String("shell", "", "optional default remote shell")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 0 {
		printError(jsonOut, "unexpected arguments: %v", positionals)
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		printError(jsonOut, "--name is required")
		return 2
	}
	if strings.TrimSpace(*host) == "" {
		printError(jsonOut, "--host is required")
		return 2
	}
	if strings.TrimSpace(*user) == "" {
		printError(jsonOut, "--user is required")
		return 2
	}
	method := sshconfig.AuthMethod(strings.TrimSpace(*auth))
	switch method {
	case sshconfig.AuthPassword:
		if strings.TrimSpace(*password) == "" {
			printError(jsonOut, "--password is required when --auth password")
			return 2
		}
	case sshconfig.AuthKey:
		if strings.TrimSpace(*keyPath) == "" {
			printError(jsonOut, "--key-path is required when --auth key")
			return 2
		}
	default:
		printError(jsonOut, "choose an auth mode with --auth %q or %q", sshconfig.AuthPassword, sshconfig.AuthKey)
		return 2
	}
	profile := &sshconfig.Profile{
		Host:         *host,
		Port:         *port,
		User:         *user,
		AuthMethod:   method,
		Password:     *password,
		KeyPath:      *keyPath,
		DefaultShell: *shell,
	}
	if err := store.SaveSSH(*name, profile); err != nil {
		printError(jsonOut, "save SSH config: %v", err)
		return 2
	}
	return printConfigSaved(jsonOut, *name, configstore.TypeSSH)
}

func configList(jsonOut bool, store *configstore.Store, args []string) int {
	fs := flag.NewFlagSet("ezterm config list", flag.ContinueOnError)
	typeFilter := fs.String("type", "", "config type: local or ssh")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 0 {
		printError(jsonOut, "unexpected arguments: %v", positionals)
		return 2
	}
	if *typeFilter != "" && *typeFilter != string(configstore.TypeLocal) && *typeFilter != string(configstore.TypeSSH) {
		printError(jsonOut, "invalid --type %q (want local or ssh)", *typeFilter)
		return 2
	}
	configs, err := store.ListAll()
	if err != nil {
		printError(jsonOut, "list configs: %v", err)
		return 2
	}
	if *typeFilter != "" {
		filtered := configs[:0]
		for _, cfg := range configs {
			if cfg.Type == *typeFilter {
				filtered = append(filtered, cfg)
			}
		}
		configs = filtered
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"configs": configs})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		printConfigTable(configs)
	}
	return 0
}

func configDelete(jsonOut bool, store *configstore.Store, args []string) int {
	fs := flag.NewFlagSet("ezterm config delete", flag.ContinueOnError)
	name := fs.String("name", "", "config name")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 0 {
		printError(jsonOut, "unexpected arguments: %v", positionals)
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		printError(jsonOut, "--name is required")
		return 2
	}
	if err := store.Delete(*name); err != nil {
		printError(jsonOut, "delete config: %v", err)
		return 2
	}
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"deleted": true, "name": *name})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("config %q deleted\n", *name)
	}
	return 0
}

func printConfigSaved(jsonOut bool, name string, typ configstore.Type) int {
	if jsonOut {
		out, _ := json.Marshal(map[string]any{
			"config": map[string]string{"name": name, "type": string(typ)},
		})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("%s config %q saved\n", typ, name)
	}
	return 0
}

func printConfigTable(configs []api.ConfigSummary) {
	if len(configs) == 0 {
		fmt.Println("no configs")
		return
	}
	const format = "%-16s  %-8s  %-28s  %-8s  %-24s  %-8s  %s\n"
	fmt.Printf(format, "NAME", "TYPE", "COMMAND", "MODE", "HOST", "USER", "DETAIL")
	for _, cfg := range configs {
		command := cfg.Command
		if command == "" && cfg.Type == string(configstore.TypeLocal) {
			command = "(default shell)"
		}
		detail := ""
		if cfg.Type == string(configstore.TypeSSH) {
			detail = cfg.AuthMethod
			if cfg.DefaultShell != "" {
				detail = strings.TrimSpace(detail + " " + cfg.DefaultShell)
			}
		}
		fmt.Printf(format, cfg.Name, cfg.Type, command, cfg.Mode, cfg.Host, cfg.User, detail)
	}
}
