package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type serverInitOptions struct {
	configPath, mode, dataDir, listen, publicURL, certFile, keyFile string
	origins                                                         []string
	nonInteractive, replace                                         bool
}

func initializeServer(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	options, err := parseServerInitOptions(args)
	if err != nil {
		return err
	}
	if !options.nonInteractive {
		if err := promptServerInit(input, output, &options); err != nil {
			return err
		}
	}
	if options.mode == "" {
		options.mode = "loopback"
	}
	if options.dataDir == "" {
		options.dataDir = filepath.Join(filepath.Dir(options.configPath), "server-data")
	}
	if options.listen == "" {
		if options.mode == "lan" {
			return errors.New("LAN initialization requires an explicit listen address")
		}
		options.listen = "127.0.0.1:7444"
	}
	if options.mode != "loopback" && options.mode != "lan" {
		return ErrUsage
	}
	if options.mode == "lan" && (options.publicURL == "" || options.certFile == "" || options.keyFile == "") {
		return errors.New("LAN initialization requires public URL, TLS certificate, and TLS key")
	}
	if options.mode == "loopback" && !isLoopbackListen(options.listen) {
		return errors.New("loopback initialization requires a loopback listen address")
	}
	if len(options.origins) == 0 && options.publicURL != "" {
		options.origins = []string{controlOrigin(options.publicURL)}
	}
	value := ConfigFile{ConfigVersion: CurrentConfigVersion, DataDir: filepath.Clean(options.dataDir), Listen: options.listen, PublicURL: options.publicURL, TLSCertFile: options.certFile, TLSKeyFile: options.keyFile, AllowedControlOrigins: append([]string(nil), options.origins...)}
	if err := ValidateConfigFile(value); err != nil {
		return errors.New("server initialization configuration is invalid")
	}
	if options.mode == "lan" {
		if _, err := loadTLSConfig(Options{Listen: value.Listen, PublicURL: value.PublicURL, TLSCertFile: value.TLSCertFile, TLSKeyFile: value.TLSKeyFile}); err != nil {
			return errors.New("server initialization TLS identity is invalid")
		}
	}
	if _, err := os.Lstat(options.configPath); err == nil && !options.replace {
		return errors.New("server configuration already exists; use --replace to overwrite it")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("server configuration path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(options.configPath), 0o700); err != nil {
		return errors.New("server configuration directory could not be created")
	}
	if err := os.Chmod(filepath.Dir(options.configPath), 0o700); err != nil {
		return errors.New("server configuration directory permissions could not be applied")
	}
	if err := os.MkdirAll(value.DataDir, 0o700); err != nil {
		return errors.New("server data directory could not be created")
	}
	if err := os.Chmod(value.DataDir, 0o700); err != nil {
		return errors.New("server data directory permissions could not be applied")
	}
	store, err := NewConfigFileStore(options.configPath)
	if err != nil || store.Save(ctx, value) != nil {
		return errors.New("server configuration could not be written")
	}
	if err := doctor(ctx, []string{"--config", options.configPath}, output); err != nil {
		return err
	}
	base := serverPublicBase(value)
	_, _ = fmt.Fprintf(output, "Workbench: %s/\nAdmin: %s/admin\nPairing: %s/pair\n", base, base, base)
	return nil
}

func parseServerInitOptions(args []string) (serverInitOptions, error) {
	var result serverInitOptions
	seen := map[string]bool{}
	for index := 0; index < len(args); index++ {
		name := args[index]
		if seen[name] && name != "--allowed-control-origin" {
			return result, ErrUsage
		}
		seen[name] = true
		next := func() (string, error) {
			index++
			if index >= len(args) {
				return "", ErrUsage
			}
			return args[index], nil
		}
		switch name {
		case "--config", "--data-dir", "--tls-cert", "--tls-key":
			value, err := next()
			if err != nil || !filepath.IsAbs(value) {
				return result, ErrUsage
			}
			value = filepath.Clean(value)
			switch name {
			case "--config":
				result.configPath = value
			case "--data-dir":
				result.dataDir = value
			case "--tls-cert":
				result.certFile = value
			case "--tls-key":
				result.keyFile = value
			}
		case "--mode", "--listen", "--public-url", "--allowed-control-origin":
			value, err := next()
			if err != nil {
				return result, ErrUsage
			}
			switch name {
			case "--mode":
				result.mode = value
			case "--listen":
				result.listen = value
			case "--public-url":
				result.publicURL = strings.TrimSuffix(value, "/")
			case "--allowed-control-origin":
				result.origins = append(result.origins, strings.TrimSuffix(value, "/"))
			}
		case "--non-interactive":
			result.nonInteractive = true
		case "--replace":
			result.replace = true
		default:
			return result, ErrUsage
		}
	}
	if result.configPath == "" {
		return result, ErrUsage
	}
	if result.nonInteractive && result.mode == "" {
		return result, errors.New("non-interactive initialization requires --mode")
	}
	return result, nil
}

func promptServerInit(input io.Reader, output io.Writer, options *serverInitOptions) error {
	reader := bufio.NewReader(input)
	read := func(label, current string) (string, error) {
		if current != "" {
			return current, nil
		}
		_, _ = fmt.Fprintf(output, "%s: ", label)
		value, err := reader.ReadString('\n')
		return strings.TrimSpace(value), err
	}
	var err error
	if options.mode, err = read("Mode (loopback/lan)", options.mode); err != nil {
		return err
	}
	if options.listen, err = read("Listen address", options.listen); err != nil {
		return err
	}
	if options.mode == "lan" {
		if options.publicURL, err = read("Public HTTPS URL", options.publicURL); err != nil {
			return err
		}
		if options.certFile, err = read("TLS certificate absolute path", options.certFile); err != nil {
			return err
		}
		if options.keyFile, err = read("TLS key absolute path", options.keyFile); err != nil {
			return err
		}
	}
	return nil
}

func serverPublicBase(value ConfigFile) string {
	if value.PublicURL != "" {
		return strings.TrimSuffix(value.PublicURL, "/")
	}
	host, port, _ := net.SplitHostPort(value.Listen)
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}
