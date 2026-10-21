package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// cmdFiles dispatches the file transfer subcommands.
func cmdFiles(c *client, jsonOut bool, args []string) int {
	if len(args) == 0 {
		printError(jsonOut, "files requires a subcommand: ls, upload, download, rm, mkdir")
		return 2
	}
	switch args[0] {
	case "ls":
		return cmdFilesList(c, jsonOut, args[1:])
	case "upload":
		return cmdFilesUpload(c, jsonOut, args[1:])
	case "download":
		return cmdFilesDownload(c, jsonOut, args[1:])
	case "rm":
		return cmdFilesRemove(c, jsonOut, args[1:])
	case "mkdir":
		return cmdFilesMkdir(c, jsonOut, args[1:])
	default:
		printError(jsonOut, "unknown files subcommand %q (want ls, upload, download, rm, mkdir)", args[0])
		return 2
	}
}

// cmdFilesList prints a directory listing or a single file's metadata.
func cmdFilesList(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm files ls", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	path := ""
	if len(positionals) > 1 {
		path = positionals[1]
	}
	listing, err := c.filesList(id, path)
	if err != nil {
		printError(jsonOut, "list files: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		return printJSON(listing)
	}
	fmt.Printf("%s\n", listing.Path)
	if len(listing.Entries) == 0 {
		fmt.Println("(empty)")
		return 0
	}
	for _, entry := range listing.Entries {
		kind := "file"
		switch {
		case entry.Dir:
			kind = "dir"
		case entry.Symlink:
			kind = "link"
		}
		size := strconv.FormatInt(entry.Size, 10)
		if entry.Dir {
			size = "-"
		}
		name := entry.Name
		if entry.Dir {
			name += "/"
		}
		fmt.Printf("%-4s %10s  %s  %s\n", kind, size, entry.Modified.Local().Format("2006-01-02 15:04"), name)
	}
	return 0
}

// cmdFilesUpload streams a local file (or stdin) to the session.
func cmdFilesUpload(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm files upload", flag.ContinueOnError)
	noOverwrite := fs.Bool("no-overwrite", false, "fail when the target already exists")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	if len(positionals) < 2 {
		printError(jsonOut, "upload requires a local source path or - for stdin")
		return 2
	}
	source := positionals[1]
	target := ""
	if len(positionals) > 2 {
		target = positionals[2]
	}

	var (
		body io.Reader
		size int64 = -1
		name       = target
	)
	if source != "-" && (strings.HasSuffix(target, "/") || strings.HasSuffix(target, "\\")) {
		name = appendToDir(target, filepath.Base(source))
	}
	if source == "-" {
		if target == "" {
			printError(jsonOut, "upload from stdin requires a target path")
			return 2
		}
		if strings.HasSuffix(target, "/") || strings.HasSuffix(target, "\\") {
			printError(jsonOut, "upload from stdin requires a file name, not just a directory")
			return 2
		}
		body = os.Stdin
	} else {
		info, err := os.Stat(source)
		if err != nil {
			printError(jsonOut, "read source: %v", err)
			return 2
		}
		if info.IsDir() {
			printError(jsonOut, "source %q is a directory; only single files are supported", source)
			return 2
		}
		f, err := os.Open(source)
		if err != nil {
			printError(jsonOut, "read source: %v", err)
			return 2
		}
		defer f.Close()
		body = f
		size = info.Size()
		if name == "" {
			name = filepath.Base(source)
		}
	}

	ctx := context.Background()
	if !jsonOut && progressEnabled() {
		body = newProgressReader(body, size, "uploading")
	}
	resp, err := c.filesUpload(ctx, id, name, body, size, !*noOverwrite)
	if err != nil {
		clearProgress()
		printError(jsonOut, "upload: %v", err)
		return exitCodeFor(err)
	}
	clearProgress()
	if jsonOut {
		return printJSON(resp)
	}
	fmt.Printf("uploaded %s (%s)\n", resp.File.Path, humanSize(resp.File.Size))
	return 0
}

// cmdFilesDownload streams a remote file to a local path or stdout.
func cmdFilesDownload(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm files download", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing local file")
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	if len(positionals) < 2 {
		printError(jsonOut, "download requires a remote path")
		return 2
	}
	source := positionals[1]
	target := ""
	if len(positionals) > 2 {
		target = positionals[2]
	}
	if target != "" && target != "-" && (strings.HasSuffix(target, "/") || strings.HasSuffix(target, "\\")) {
		base := remoteBase(source)
		if !validBase(base) {
			printError(jsonOut, "cannot derive a local file name from %q; pass a target path", source)
			return 2
		}
		target = appendToDir(target, base)
	}
	toStdout := target == "-"
	if toStdout && jsonOut {
		printError(jsonOut, "cannot combine --json with - (stdout); pass a local target path")
		return 2
	}
	if target == "" {
		target = remoteBase(source)
		if !validBase(target) {
			printError(jsonOut, "cannot derive a local file name from %q; pass a target path", source)
			return 2
		}
	}
	if !toStdout && !*force {
		if _, err := os.Stat(target); err == nil {
			printError(jsonOut, "%s already exists (use --force to overwrite)", target)
			return 2
		}
	}

	resp, err := c.filesDownload(context.Background(), id, source)
	if err != nil {
		printError(jsonOut, "download: %v", err)
		return exitCodeFor(err)
	}
	defer resp.Body.Close()

	var (
		out  io.Writer
		file *os.File
	)
	if toStdout {
		out = os.Stdout
	} else {
		// Write next to the target first so an interrupted transfer never
		// leaves a truncated file in place.
		dir := filepath.Dir(target)
		temp, err := os.CreateTemp(dir, ".ezterm-download-*")
		if err != nil {
			printError(jsonOut, "create target: %v", err)
			return 2
		}
		file = temp
		out = temp
	}
	reader := io.Reader(resp.Body)
	if !jsonOut && !toStdout && progressEnabled() {
		reader = newProgressReader(reader, resp.ContentLength, "downloading")
	}
	written, copyErr := io.Copy(out, reader)
	clearProgress()
	if file != nil {
		closeErr := file.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(file.Name())
		} else if err := replaceFile(file.Name(), target); err != nil {
			_ = os.Remove(file.Name())
			copyErr = err
		}
	}
	if copyErr != nil {
		printError(jsonOut, "download: %v", copyErr)
		return 2
	}
	if jsonOut {
		return printJSON(map[string]any{"path": source, "local": target, "bytes": written})
	}
	if toStdout {
		return 0
	}
	fmt.Printf("downloaded %s (%s)\n", target, humanSize(written))
	return 0
}

// cmdFilesRemove deletes a file, symlink, or empty directory.
func cmdFilesRemove(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm files rm", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	if len(positionals) < 2 {
		printError(jsonOut, "rm requires a path")
		return 2
	}
	path := positionals[1]
	if err := c.filesDelete(id, path); err != nil {
		printError(jsonOut, "remove: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		return printJSON(map[string]string{"removed": path})
	}
	fmt.Printf("removed %s\n", path)
	return 0
}

// cmdFilesMkdir creates one directory.
func cmdFilesMkdir(c *client, jsonOut bool, args []string) int {
	fs := flag.NewFlagSet("ezterm files mkdir", flag.ContinueOnError)
	positionals := parseCommandArgs(fs, args)
	if positionals == nil {
		return 2
	}
	id, err := parseIDArg(positionals)
	if err != nil {
		printError(jsonOut, "%v", err)
		return 2
	}
	if len(positionals) < 2 {
		printError(jsonOut, "mkdir requires a path")
		return 2
	}
	path := positionals[1]
	resp, err := c.filesMkdir(id, path)
	if err != nil {
		printError(jsonOut, "mkdir: %v", err)
		return exitCodeFor(err)
	}
	if jsonOut {
		return printJSON(resp)
	}
	fmt.Printf("created %s\n", resp.File.Path)
	return 0
}

// remoteBase returns the last element of a POSIX or native path.
func remoteBase(p string) string {
	return filepath.Base(strings.ReplaceAll(p, "\\", "/"))
}

// validBase reports whether base can name a file the CLI may create.
func validBase(base string) bool {
	return base != "" && base != "." && base != ".." && base != "/" && base != "\\"
}

// appendToDir joins name to a directory spelled with a trailing separator,
// keeping the separator style the user typed.
func appendToDir(dir, name string) string {
	if strings.HasSuffix(dir, "\\") {
		return strings.TrimRight(dir, "\\") + "\\" + name
	}
	return strings.TrimRight(dir, "/") + "/" + name
}

// printJSON writes a compact JSON document to stdout and returns an exit code.
func printJSON(v any) int {
	encoded, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ezterm: encode output: "+err.Error())
		return 2
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return 0
}

// replaceFile publishes temp at target, tolerating platforms where rename
// cannot replace an existing file (Windows).
func replaceFile(temp, target string) error {
	if err := os.Rename(temp, target); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temp, target)
}

// progressEnabled reports whether a single-line progress indicator fits: the
// destination must be a terminal, so pipes and --json stay clean.
func progressEnabled() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// progressReader renders transfer progress on stderr while copying.
type progressReader struct {
	reader io.Reader
	total  int64
	label  string
	done   int64
	last   time.Time
}

// newProgressReader wraps r, showing progress against total (-1 = unknown).
func newProgressReader(r io.Reader, total int64, label string) *progressReader {
	p := &progressReader{reader: r, total: total, label: label, last: time.Now()}
	p.render(false)
	return p
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.render(false)
	}
	if err != nil {
		p.render(true)
	}
	return n, err
}

// render redraws the progress line at most 20 times a second.
func (p *progressReader) render(final bool) {
	if !final && time.Since(p.last) < 50*time.Millisecond {
		return
	}
	p.last = time.Now()
	if p.total > 0 {
		percent := float64(p.done) / float64(p.total) * 100
		fmt.Fprintf(os.Stderr, "\r%s %s / %s (%.0f%%)   ", p.label, humanSize(p.done), humanSize(p.total), percent)
		return
	}
	fmt.Fprintf(os.Stderr, "\r%s %s   ", p.label, humanSize(p.done))
}

// clearProgress erases the progress line.
func clearProgress() {
	if progressEnabled() {
		fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", 78)+"\r")
	}
}

// humanSize formats a byte count for progress and summaries.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes) / unit
	index := 0
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}
