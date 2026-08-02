package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/yuanshu-ai/yuanshu/internal/node"
	"github.com/yuanshu-ai/yuanshu/internal/server"
	"github.com/yuanshu-ai/yuanshu/internal/standalone"
)

const usage = `Yuanshu · 远枢

Usage:
  yuanshu <command>
  yuanshu <command> --help

Commands:
  server       Run the Web, control plane, and relay
  node         Run the local Node bridge
  standalone   Run Server, Web, and the local Node together

Server, Node, and Standalone are formal Alpha entry points.
`

var commandDescriptions = map[string]string{
	"server":     "Run the Web, control plane, and relay",
	"node":       "Run the local Node bridge",
	"standalone": "Run Server, Web, and the local Node together",
}

// Runner is the execution boundary between the CLI and a Yuanshu role.
type Runner func(context.Context, []string, io.Writer, io.Writer) error

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
	_, known := commandDescriptions[command]
	if !known {
		fmt.Fprintf(stderr, "error: unknown command %q\n", command)
		fmt.Fprint(stderr, usage)
		return 2
	}

	if len(args) == 2 && isHelp(args[1]) {
		if command == "node" {
			fmt.Fprint(stdout, node.Usage)
			return 0
		}
		if command == "server" {
			fmt.Fprint(stdout, server.Usage)
			return 0
		}
		fmt.Fprint(stdout, standalone.Usage)
		return 0
	}

	runner := selectRunner(command, runners)
	if runner == nil {
		fmt.Fprintf(stderr, "%s: not implemented\n", command)
		return 1
	}

	if err := runner(ctx, args[1:], stdout, stderr); err != nil {
		if errors.Is(err, node.ErrUsage) || errors.Is(err, server.ErrUsage) || errors.Is(err, standalone.ErrUsage) {
			fmt.Fprintf(stderr, "error: invalid arguments for %q\n", command)
			if command == "server" {
				fmt.Fprint(stderr, server.Usage)
			} else if command == "standalone" {
				fmt.Fprint(stderr, standalone.Usage)
			} else {
				fmt.Fprint(stderr, node.Usage)
			}
			return 2
		}
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
