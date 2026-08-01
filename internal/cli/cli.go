package cli

import (
	"context"
	"fmt"
	"io"
)

const usage = `Yuanshu · 远枢

Usage:
  yuanshu <command>
  yuanshu <command> --help

Commands:
  server       Run the Web, control plane, and relay
  node         Run the local Node bridge
  standalone   Run Server, Web, and the local Node together

The temporary M0 PoC requires explicit YUANSHU_POC_* environment configuration.
`

var commandDescriptions = map[string]string{
	"server":     "Run the Web, control plane, and relay",
	"node":       "Run the local Node bridge",
	"standalone": "Run Server, Web, and the local Node together",
}

// Runner is the smallest execution boundary between the CLI and a Yuanshu role.
type Runner func(context.Context) error

// Runners contains the role entry points selected by the CLI.
type Runners struct {
	Server     Runner
	Node       Runner
	Standalone Runner
}

// Run parses CLI arguments and invokes exactly one selected role.
func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	runners Runners,
) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}

	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: a command is required")
		fmt.Fprint(stderr, usage)
		return 2
	}

	command := args[0]
	description, known := commandDescriptions[command]
	if !known {
		fmt.Fprintf(stderr, "error: unknown command %q\n", command)
		fmt.Fprint(stderr, usage)
		return 2
	}

	if len(args) == 2 && isHelp(args[1]) {
		fmt.Fprintf(stdout, "Usage: yuanshu %s\n\n%s.\n\nThis temporary M0 PoC requires explicit YUANSHU_POC_* environment configuration.\n", command, description)
		return 0
	}

	if len(args) != 1 {
		fmt.Fprintf(stderr, "error: unexpected arguments for %q\n", command)
		return 2
	}

	runner := selectRunner(command, runners)
	if runner == nil {
		fmt.Fprintf(stderr, "%s: not implemented\n", command)
		return 1
	}

	if err := runner(ctx); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return 1
	}

	return 0
}

func isHelp(arg string) bool {
	return arg == "--help" || arg == "-h"
}

func selectRunner(command string, runners Runners) Runner {
	switch command {
	case "server":
		return runners.Server
	case "node":
		return runners.Node
	case "standalone":
		return runners.Standalone
	default:
		return nil
	}
}
