//go:build linux

package cli

import "os/exec"

// openURL opens url in the user's default browser on Linux.
func openURL(url string) error {
	cmd := exec.Command("xdg-open", url)
	return cmd.Start()
}
