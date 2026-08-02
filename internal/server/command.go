package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Usage = `Usage:
  yuanshu server [run] --data-dir <absolute-path> [--listen <ip:port>]
    [--public-url https://host[:port] --tls-cert <absolute-path> --tls-key <absolute-path>]
  yuanshu server healthcheck [--address 127.0.0.1:7444]
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
	if len(args) > 0 && args[0] == "healthcheck" {
		return healthcheck(ctx, args[1:])
	}
	options, err := parseServerOptions(args)
	if err != nil {
		return err
	}
	options.Stdout = stdout
	return Run(ctx, options)
}

func parseServerArguments(args []string) (string, string, error) {
	options, err := parseServerOptions(args)
	return options.DataDir, options.Listen, err
}

func parseServerOptions(args []string) (Options, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "run" {
			return Options{}, ErrUsage
		}
		args = args[1:]
	}
	options := Options{Listen: "127.0.0.1:7444"}
	seen := make(map[string]bool)
	for index := 0; index < len(args); index++ {
		name := args[index]
		if seen[name] {
			return Options{}, ErrUsage
		}
		seen[name] = true
		switch name {
		case "--data-dir":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.DataDir = filepath.Clean(args[index])
		case "--listen":
			index++
			if index >= len(args) {
				return Options{}, ErrUsage
			}
			options.Listen = args[index]
		case "--public-url":
			index++
			if index >= len(args) {
				return Options{}, ErrUsage
			}
			options.PublicURL = args[index]
		case "--tls-cert":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.TLSCertFile = filepath.Clean(args[index])
		case "--tls-key":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.TLSKeyFile = filepath.Clean(args[index])
		default:
			return Options{}, ErrUsage
		}
	}
	if options.DataDir == "" || !validListen(options.Listen) || !validPublicOptions(options) {
		return Options{}, ErrUsage
	}
	return options, nil
}

func validListen(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	if net.ParseIP(host) == nil {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort > 0 && parsedPort <= 65535
}

func validPublicOptions(options Options) bool {
	tlsCount := 0
	for _, value := range []string{options.PublicURL, options.TLSCertFile, options.TLSKeyFile} {
		if value != "" {
			tlsCount++
		}
	}
	if tlsCount != 0 && tlsCount != 3 {
		return false
	}
	host, _, err := net.SplitHostPort(options.Listen)
	if err != nil {
		return false
	}
	if host != "127.0.0.1" && host != "::1" && tlsCount != 3 {
		return false
	}
	if options.PublicURL == "" {
		return true
	}
	parsed, err := url.Parse(options.PublicURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

func publicURLHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func healthcheck(ctx context.Context, args []string) error {
	address := "127.0.0.1:7444"
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--address" || !validListen(args[1]) {
			return ErrUsage
		}
		address = args[1]
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("server healthcheck failed")
	}
	return connection.Close()
}
