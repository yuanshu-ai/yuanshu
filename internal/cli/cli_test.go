package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestHelpDoesNotInvokeRunners(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--help"},
		{"server", "--help"},
		{"node", "--help"},
		{"standalone", "--help"},
	} {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Parallel()

			calls := 0
			runner := func(context.Context, []string, io.Writer, io.Writer) error {
				calls++
				return nil
			}
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)

			exitCode := Run(context.Background(), args, stdout, stderr, Runners{
				Server: runner, Node: runner, Standalone: runner,
			})

			if exitCode != 0 {
				t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
			}
			if calls != 0 {
				t.Fatalf("help invoked a runner %d times", calls)
			}
			if stdout.Len() == 0 {
				t.Fatal("help output is empty")
			}
		})
	}
}

func TestMissingAndUnknownCommandsExitWithUsageError(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"unknown"}, {"standalone", "unexpected"}} {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Parallel()

			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			exitCode := Run(context.Background(), args, stdout, stderr, Runners{})

			if exitCode != 2 {
				t.Fatalf("Run() exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr.String(), "error:") {
				t.Fatalf("stderr = %q, want an explicit error", stderr.String())
			}
		})
	}
}

func TestInvalidNodeArgumentsExitWithUsageError(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	exitCode := Run(context.Background(), []string{"node", "unknown"}, stdout, stderr, DefaultRunners())
	if exitCode != 2 || !strings.Contains(stderr.String(), "yuanshu node") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestInvalidServerArgumentsExitWithUsageError(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	exitCode := Run(context.Background(), []string{"server", "unknown"}, stdout, stderr, DefaultRunners())
	if exitCode != 2 || !strings.Contains(stderr.String(), "yuanshu server") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestBareCommandInvokesOnlySelectedRunner(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"server", "node", "standalone"} {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			calls := map[string]int{}
			runners := Runners{
				Server: func(context.Context, []string, io.Writer, io.Writer) error { calls["server"]++; return nil },
				Node:   func(context.Context, []string, io.Writer, io.Writer) error { calls["node"]++; return nil },
				Standalone: func(context.Context, []string, io.Writer, io.Writer) error {
					calls["standalone"]++
					return nil
				},
			}

			exitCode := Run(context.Background(), []string{command}, new(bytes.Buffer), new(bytes.Buffer), runners)

			if exitCode != 0 {
				t.Fatalf("Run() exit code = %d, want 0", exitCode)
			}
			for name, count := range calls {
				want := 0
				if name == command {
					want = 1
				}
				if count != want {
					t.Fatalf("runner %s called %d times, want %d", name, count, want)
				}
			}
		})
	}
}

func TestRunnerErrorIsReported(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("runner failed")
	stderr := new(bytes.Buffer)
	exitCode := Run(context.Background(), []string{"server"}, new(bytes.Buffer), stderr, Runners{
		Server: func(context.Context, []string, io.Writer, io.Writer) error { return wantErr },
	})

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want runner error", stderr.String())
	}
}

func TestDefaultServerRequiresExplicitDataDirectory(t *testing.T) {
	stderr := new(bytes.Buffer)
	exitCode := Run(context.Background(), []string{"server"}, new(bytes.Buffer), stderr, DefaultRunners())
	if exitCode != 2 || !strings.Contains(stderr.String(), "--data-dir") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestDefaultStandaloneFailsSafelyWithoutPOCConfiguration(t *testing.T) {
	stderr := new(bytes.Buffer)
	exitCode := Run(context.Background(), []string{"standalone"}, new(bytes.Buffer), stderr, DefaultRunners())
	if exitCode != 1 || !strings.Contains(stderr.String(), "PoC configuration is not available") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}
