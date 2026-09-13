package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUsageExit(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "p")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s", out)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "GROK_HOME="+t.TempDir())
	if err := cmd.Run(); err == nil {
		t.Fatal("expected nonzero")
	}
}
