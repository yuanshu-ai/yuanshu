//go:build darwin

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinProcessManagerLifecycle(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := newDarwinProcessManager().Start(context.Background(), ProcessSpec{
		Executable: executable,
		Args:       []string{"-test.run=TestDarwinProcessHelper", "--"},
		Env:        append(os.Environ(), "YUANSHU_DARWIN_PROCESS_HELPER=echo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(process.Stdin(), "synthetic-input"); err != nil {
		t.Fatal(err)
	}
	_ = process.Stdin().Close()
	stdout, err := io.ReadAll(process.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(process.Stderr())
	if err != nil {
		t.Fatal(err)
	}
	exit, err := process.Wait(context.Background())
	if err != nil || exit.Code != 7 || exit.Forced {
		t.Fatalf("exit = %#v, error = %v", exit, err)
	}
	if string(stdout) != "synthetic-input" || string(stderr) != "synthetic-stderr" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestDarwinProcessManagerEscalatesToKill(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := newDarwinProcessManager().Start(context.Background(), ProcessSpec{
		Executable: executable,
		Args:       []string{"-test.run=TestDarwinProcessHelper", "--"},
		Env:        append(os.Environ(), "YUANSHU_DARWIN_PROCESS_HELPER=ignore-term"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	exit, err := process.Stop(ctx)
	if err != nil || !exit.Forced {
		t.Fatalf("Stop() = %#v, %v", exit, err)
	}
	if repeated, err := process.Stop(context.Background()); err != nil || repeated != exit {
		t.Fatalf("repeated Stop() = %#v, %v", repeated, err)
	}
}

func TestDarwinProcessManagerErrorsAreRedacted(t *testing.T) {
	const canary = "process-path-canary"
	_, err := newDarwinProcessManager().Start(context.Background(), ProcessSpec{
		Executable: canary, Args: []string{canary}, Env: []string{"SECRET=" + canary}, Directory: canary,
	})
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("Start() error = %v", err)
	}
	for _, executable := range []string{"/tmp/codex.sh", "/tmp/codex.SH"} {
		if _, err := newDarwinProcessManager().Start(context.Background(), ProcessSpec{Executable: executable}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Start(%q) = %v", executable, err)
		}
	}
}

func TestDarwinProcessHelper(t *testing.T) {
	if os.Getenv("YUANSHU_DARWIN_PROCESS_HELPER") == "" {
		return
	}
	switch os.Getenv("YUANSHU_DARWIN_PROCESS_HELPER") {
	case "echo":
		input, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdout.Write(input)
		_, _ = io.WriteString(os.Stderr, "synthetic-stderr")
		os.Exit(7)
	case "ignore-term":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
		for range signals {
		}
	default:
		os.Exit(9)
	}
}
