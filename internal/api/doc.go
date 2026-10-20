// Package api contains JSON wire types shared by the daemon and CLI.
// Session.WebURL is populated for sessions whose Web terminal is enabled: the
// default for PTY sessions unless CreateSessionRequest.Web disables it. The URL
// points to the daemon's local embedded Web terminal.
package api
