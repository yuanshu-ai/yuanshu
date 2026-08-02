package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const Usage = `Usage:
  yuanshu server [run] --data-dir <absolute-path> [--listen 127.0.0.1:7444]
`

var ErrUsage = errors.New("server command arguments are invalid")

func Command(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if ctx == nil {
		return context.Canceled
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, Usage)
		return nil
	}
	dataDir, listen, err := parseServerArguments(args)
	if err != nil {
		return err
	}
	return Run(ctx, Options{DataDir: dataDir, Listen: listen, Stdout: stdout})
}

func parseServerArguments(args []string) (string, string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "run" {
			return "", "", ErrUsage
		}
		args = args[1:]
	}
	listen := "127.0.0.1:7444"
	var dataDir string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) || dataDir != "" {
				return "", "", ErrUsage
			}
			dataDir = filepath.Clean(args[index])
		case "--listen":
			index++
			if index >= len(args) {
				return "", "", ErrUsage
			}
			listen = args[index]
		default:
			return "", "", ErrUsage
		}
	}
	if dataDir == "" || !validListen(listen) {
		return "", "", ErrUsage
	}
	return dataDir, listen, nil
}

func validListen(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort > 0 && parsedPort <= 65535
}
