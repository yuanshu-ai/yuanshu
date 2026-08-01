//go:build windows

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWindowsProcessManagerLifecycle(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := newWindowsProcessManager()
	process, err := manager.Start(context.Background(), ProcessSpec{
		Executable: executable,
		Args:       []string{"-test.run=TestWindowsProcessHelper", "--"},
		Env:        append(os.Environ(), "YUANSHU_PROCESS_HELPER=echo"),
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
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 7 || exit.Forced {
		t.Fatalf("exit = %#v", exit)
	}
	if string(stdout) != "synthetic-input" || string(stderr) != "synthetic-stderr" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if repeated, err := process.Wait(context.Background()); err != nil || repeated != exit {
		t.Fatalf("repeated Wait = %#v, %v", repeated, err)
	}
}

func TestWindowsProcessManagerStopAndWaitTimeout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := newWindowsProcessManager().Start(context.Background(), ProcessSpec{
		Executable: executable,
		Args:       []string{"-test.run=TestWindowsProcessHelper", "--"},
		Env:        append(os.Environ(), "YUANSHU_PROCESS_HELPER=wait"),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := process.Wait(waitCtx); err != context.DeadlineExceeded {
		t.Fatalf("Wait timeout error = %v", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	exit, err := process.Stop(stopCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !exit.Forced {
		t.Fatalf("forced exit = %#v", exit)
	}
	if repeated, err := process.Stop(stopCtx); err != nil || repeated != exit {
		t.Fatalf("repeated Stop = %#v, %v", repeated, err)
	}
}

func TestWindowsProcessManagerErrorsAreRedacted(t *testing.T) {
	const canary = "process-path-canary"
	_, err := newWindowsProcessManager().Start(context.Background(), ProcessSpec{
		Executable: canary,
		Args:       []string{canary},
		Env:        []string{"SECRET=" + canary},
		Directory:  canary,
	})
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error = %v", err)
	}
}

func TestWindowsProcessManagerRejectsShellScripts(t *testing.T) {
	for _, executable := range []string{`C:\synthetic\codex.cmd`, `C:\synthetic\codex.bat`, `C:\synthetic\codex.ps1`} {
		_, err := newWindowsProcessManager().Start(context.Background(), ProcessSpec{Executable: executable})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Start(%q) error = %v", executable, err)
		}
	}
}

func TestWindowsProcessHelper(t *testing.T) {
	mode := os.Getenv("YUANSHU_PROCESS_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		input, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdout.Write(input)
		_, _ = io.WriteString(os.Stderr, "synthetic-stderr")
		os.Exit(7)
	case "wait":
		time.Sleep(time.Hour)
	default:
		os.Exit(9)
	}
}
