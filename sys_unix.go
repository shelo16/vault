//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func stty(arg string) {
	c := exec.Command("stty", arg)
	c.Stdin = os.Stdin
	_ = c.Run()
}

// readPasswordTTY reads a line from the terminal with echo turned off.
func readPasswordTTY(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if !isTerminal(os.Stdin) {
		return readLine("")
	}
	stty("-echo")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			stty("echo")
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	line, err := readLine("")
	close(done)
	signal.Stop(sig)
	stty("echo")
	fmt.Fprintln(os.Stderr)
	return line, err
}

// detach starts a helper that survives the terminal closing.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func hideWindow(cmd *exec.Cmd) {}
