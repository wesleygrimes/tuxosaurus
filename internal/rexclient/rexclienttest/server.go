// Package rexclienttest starts an isolated Tuxosaurus server for client
// integration tests. It runs the server as a separate executable so the
// client packages stay free of cgo.
package rexclienttest

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// StartServer launches binary on a temporary Unix socket with temporary
// state and an empty Lua config, and returns the unix:// URL. The server is
// stopped when the test ends; its log is printed if the test failed.
func StartServer(t testing.TB, binary string) string {
	t.Helper()
	root := t.TempDir()
	// Unix socket paths are limited to about 100 bytes; TempDir can exceed it.
	sockDir, err := os.MkdirTemp("", "rexctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	config := filepath.Join(root, "init.lua")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	logfile, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--listen", "unix://"+sock, "--state-dir", filepath.Join(root, "state"), "--shell", "/bin/sh", "--config", config)
	cmd.Stdout, cmd.Stderr = logfile, logfile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			b, _ := os.ReadFile(logfile.Name())
			t.Logf("server log:\n%s", b)
		}
		logfile.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server socket did not appear")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "unix://" + sock
}
