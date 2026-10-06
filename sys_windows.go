//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const (
	enableEchoInput       = 0x0004
	createNoWindow        = 0x08000000
	createNewProcessGroup = 0x00000200
)

func isTerminal(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func readPasswordTTY(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return readLine("")
	}
	procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput))
	defer procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	line, err := readLine("")
	fmt.Fprintln(os.Stderr)
	return line, err
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | createNewProcessGroup}
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
