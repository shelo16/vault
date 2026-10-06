package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Clipboard access uses each OS's own tools so the binary has no dependencies:
//   macOS:   pbcopy / pbpaste
//   Windows: PowerShell Set-Clipboard / Get-Clipboard
//   Linux:   wl-copy / wl-paste (Wayland), xclip or xsel (X11)

type clipTool struct {
	set, get, clear []string
}

func psCmd(script string) []string {
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-Command", script}
}

func clipboardTool() (*clipTool, error) {
	switch runtime.GOOS {
	case "darwin":
		return &clipTool{set: []string{"pbcopy"}, get: []string{"pbpaste"}, clear: []string{"pbcopy"}}, nil
	case "windows":
		return &clipTool{
			set:   psCmd(`[Console]::InputEncoding=[Text.UTF8Encoding]::new($false); $v=[Console]::In.ReadToEnd(); Set-Clipboard -Value $v`),
			get:   psCmd(`[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); [Console]::Out.Write((Get-Clipboard -Raw))`),
			clear: psCmd(`Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.Clipboard]::Clear()`),
		}, nil
	}
	has := func(n string) bool { _, err := exec.LookPath(n); return err == nil }
	if os.Getenv("WAYLAND_DISPLAY") != "" && has("wl-copy") {
		return &clipTool{set: []string{"wl-copy"}, get: []string{"wl-paste", "-n"}, clear: []string{"wl-copy", "--clear"}}, nil
	}
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		if has("xclip") {
			return &clipTool{set: []string{"xclip", "-selection", "clipboard", "-i"}, get: []string{"xclip", "-selection", "clipboard", "-o"}, clear: []string{"xclip", "-selection", "clipboard", "-i"}}, nil
		}
		if has("xsel") {
			return &clipTool{set: []string{"xsel", "-b", "-i"}, get: []string{"xsel", "-b", "-o"}, clear: []string{"xsel", "-b", "-c"}}, nil
		}
		return nil, errors.New("no clipboard tool found — install one: `sudo apt install wl-clipboard` (Wayland) or `sudo apt install xclip` (X11)")
	}
	return nil, errors.New("no clipboard here (no graphical session, e.g. over SSH)")
}

func runClip(argv []string, input string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	hideWindow(cmd)
	cmd.Stdin = strings.NewReader(input)
	// Stdout/Stderr stay nil (/dev/null): xclip/wl-copy fork a background process
	// that keeps serving the clipboard, and a pipe would make us wait for it.
	return cmd.Run()
}

func clipSet(v string) error {
	t, err := clipboardTool()
	if err != nil {
		return err
	}
	if err := runClip(t.set, v); err != nil {
		return errors.New("could not write to the clipboard: " + err.Error())
	}
	return nil
}

func clipGet() (string, error) {
	t, err := clipboardTool()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(t.get[0], t.get[1:]...)
	hideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

func clipClear() {
	if t, err := clipboardTool(); err == nil {
		_ = runClip(t.clear, "")
	}
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// copyToClipboard sets the clipboard now and leaves a small background helper
// that clears it after ttl seconds — but only if it still holds our value.
func copyToClipboard(value string, ttl int) error {
	if value == "" {
		return errors.New("that field is empty")
	}
	if err := clipSet(value); err != nil {
		return err
	}
	if ttl <= 0 {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	cmd := exec.Command(exe, "__clip", strconv.Itoa(ttl))
	detach(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	// only a hash crosses over, never the value itself
	io.WriteString(in, hashHex(value))
	in.Close()
	cmd.Process.Release()
	return nil
}

func runClipChild(args []string) {
	ttl := 30
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil {
			ttl = n
		}
	}
	want, _ := io.ReadAll(io.LimitReader(os.Stdin, 128))
	time.Sleep(time.Duration(ttl) * time.Second)
	cur, err := clipGet()
	if err != nil {
		return
	}
	// some tools add/strip a trailing newline; compare both ways
	if bytes.Equal([]byte(hashHex(cur)), want) || bytes.Equal([]byte(hashHex(strings.TrimRight(cur, "\r\n"))), want) {
		clipClear()
	}
}
