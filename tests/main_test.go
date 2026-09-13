package proxy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUsageExit(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "..")
	exe := filepath.Join(t.TempDir(), "p")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s", out)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "GROK_HOME="+t.TempDir())
	if err := cmd.Run(); err == nil {
		t.Fatal("expected nonzero")
	}
}
