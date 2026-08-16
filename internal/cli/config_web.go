package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// webConfigURL is the local URL of the embedded configuration page.
func webConfigURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/config", port)
}

// cmdConfigWeb surfaces the embedded configuration page. It is the one config
// subcommand that requires a running daemon (unlike local/ssh/list/delete,
// which edit config files directly): the page and its CRUD API are served by
// the daemon.
func cmdConfigWeb(jsonOut bool, dataDir string, port int, logLevel string, args []string) int {
	fs := flag.NewFlagSet("ezterm config web", flag.ContinueOnError)
	openBrowser := fs.Bool("open", false, "open the configuration page in the default browser")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	if len(positionals) != 0 {
		printError(jsonOut, "unexpected arguments: %v", positionals)
		return 2
	}

	if err := ensureDaemon(port, dataDir, logLevel); err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}

	url := webConfigURL(port)
	if jsonOut {
		out, _ := json.Marshal(map[string]any{"url": url})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Printf("configuration page: %s\n", url)
	}
	if *openBrowser {
		if err := openURL(url); err != nil {
			if jsonOut {
				printError(true, "open browser: %v", err)
			} else {
				fmt.Println("open the URL above in your browser: " + err.Error())
			}
			return 2
		}
	}
	return 0
}
