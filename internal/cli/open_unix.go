//go:build !windows && !linux

package cli

import "os/exec"

// openURL opens url in the user's default browser on macOS (and other Unix
// systems that provide `open`, falling back to xdg-open on Linux has its own
// file, so this build tag covers darwin and BSD-like systems).
func openURL(url string) error {
	cmd := exec.Command("open", url)
	return cmd.Start()
}
