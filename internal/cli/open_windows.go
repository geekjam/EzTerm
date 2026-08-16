//go:build windows

package cli

import "os/exec"

// openURL opens url in the user's default browser on Windows.
func openURL(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	return cmd.Start()
}
