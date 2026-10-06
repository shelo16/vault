package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
)

const (
	updateRepo    = "shelo16/vault"
	updateAPIURL  = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	updateDLBase  = "https://github.com/" + updateRepo + "/releases/latest/download/"
)

func updateBinaryName() (string, error) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64":
			return "vault-linux-" + goarch, nil
		}
	case "darwin":
		switch goarch {
		case "amd64", "arm64":
			return "vault-macos-" + goarch, nil
		}
	case "windows":
		return "vault.exe", nil
	}
	return "", fmt.Errorf("unsupported platform %s/%s — update manually", goos, goarch)
}

func fetchLatestTag() (string, error) {
	resp, err := http.Get(updateAPIURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

func cmdUpdate() error {
	status("checking for updates ...")
	latest, err := fetchLatestTag()
	if err != nil {
		return fmt.Errorf("could not check for updates: %v", err)
	}
	if latest == version {
		status("already up to date (vault %s)", version)
		return nil
	}
	status("vault %s → %s — downloading ...", version, latest)

	name, err := updateBinaryName()
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate own path: %v", err)
	}

	resp, err := http.Get(updateDLBase + name)
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("cannot write update: %v", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download failed: %v", err)
	}
	f.Close()

	// On Windows, a running .exe cannot be overwritten — rename old one first.
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		_ = os.Rename(exe, old)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot replace binary: %v\n  tip: try again with sudo / as administrator", err)
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}

	status("updated to vault %s", latest)
	return nil
}
