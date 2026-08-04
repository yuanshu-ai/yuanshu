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
	tlsTermination, acmeEnvironment, acmeEmail                      string
	origins                                                         []string
	nonInteractive, replace, acceptTerms                            bool
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
		options.mode = "local"
	}
	if options.mode == "loopback" {
		options.mode = "local"
	}
	if options.mode == "lan" {
		options.mode, options.tlsTermination = "external", "server"
	}
	if options.dataDir == "" {
		options.dataDir = filepath.Join(filepath.Dir(options.configPath), "server-data")
	}
	if options.listen == "" {
		if options.mode != "local" {
			return errors.New("remote initialization requires an explicit listen address")
		}
		options.listen = "127.0.0.1:7444"
	}
	if options.mode != "local" && options.mode != "lan-managed" && options.mode != "public-ip-acme" && options.mode != "external" {
		return ErrUsage
	}
	if options.mode == "external" && options.tlsTermination == "" {
		if options.certFile != "" || options.keyFile != "" {
			options.tlsTermination = "server"
		} else {
			return errors.New("external initialization requires --tls-termination")
		}
	}
	if options.mode == "local" && !isExactLoopbackListen(options.listen) {
		return errors.New("local initialization requires a literal loopback listen address")
	}
	if len(options.origins) == 0 && options.publicURL != "" {
		options.origins = []string{controlOrigin(options.publicURL)}
	}
	value := ConfigFile{ConfigVersion: CurrentConfigVersion, DataDir: filepath.Clean(options.dataDir), Listen: options.listen, PublicURL: options.publicURL, AllowedControlOrigins: append([]string(nil), options.origins...)}
	switch options.mode {
	case "local":
		value.DeploymentMode = DeploymentLocal
	case "lan-managed":
		value.DeploymentMode = DeploymentLANManaged
	case "public-ip-acme":
		value.DeploymentMode = DeploymentPublicIPACME
		value.ACME = ACMEConfig{Environment: options.acmeEnvironment, Email: options.acmeEmail, AcceptTerms: options.acceptTerms}
	case "external":
		value.DeploymentMode = DeploymentExternal
		value.TLS = TLSFileConfig{Termination: options.tlsTermination, CertFile: options.certFile, KeyFile: options.keyFile}
	}
	if err := ValidateConfigFile(value); err != nil {
		return errors.New("server initialization configuration is invalid")
	}
	if value.DeploymentMode == DeploymentExternal && value.TLS.Termination == "server" {
		if _, err := loadTLSConfig(Options{Listen: value.Listen, DeploymentMode: value.DeploymentMode, PublicURL: value.PublicURL, TLSCertFile: value.TLS.CertFile, TLSKeyFile: value.TLS.KeyFile, TLSTermination: value.TLS.Termination}); err != nil {
			return errors.New("server initialization TLS identity is invalid")
		}
	}
	var previous ConfigFile
	if _, err := os.Lstat(options.configPath); err == nil {
		if !options.replace {
			return errors.New("server configuration already exists; use --replace to overwrite it")
		}
		previous, err = LoadConfigFile(options.configPath)
		if err != nil {
			return errors.New("existing server configuration is unavailable")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
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
	locks, err := acquireInitializationLocks(previous.DataDir, value.DataDir)
	if err != nil {
		return errors.New("server must be stopped before initialization")
	}
	defer closeInitializationLocks(locks)
	managedSwap, err := stageManagedCertificate(ctx, value)
	if err != nil {
		return err
	}
	if managedSwap != nil {
		defer managedSwap.cleanup()
		if err := managedSwap.install(); err != nil {
			return errors.New("managed certificate initialization failed")
		}
	}
	store, err := NewConfigFileStore(options.configPath)
	if err != nil || store.Save(ctx, value) != nil {
		if managedSwap != nil {
			_ = managedSwap.rollback()
		}
		return errors.New("server configuration could not be written")
	}
	if managedSwap != nil {
		managedSwap.commit()
	}
	if value.DeploymentMode == DeploymentPublicIPACME {
		_, _ = fmt.Fprintln(output, "Certificate: pending initial ACME issuance; start the Server with public TCP 443 forwarded to the configured listener.")
	} else if err := doctor(ctx, []string{"--config", options.configPath}, output); err != nil {
		return err
	}
	base := serverPublicBase(value)
	_, _ = fmt.Fprintf(output, "Workbench: %s/\nAdmin: %s/admin\nPairing: %s/pair\n", base, base, base)
	return nil
}

func acquireInitializationLocks(previousDataDir, nextDataDir string) ([]*dataLock, error) {
	paths := make([]string, 0, 2)
	for _, dataDir := range []string{previousDataDir, nextDataDir} {
		if dataDir == "" {
			continue
		}
		path := filepath.Join(filepath.Clean(dataDir), "server.lock")
		seen := false
		for _, existing := range paths {
			if existing == path {
				seen = true
				break
			}
		}
		if !seen {
			paths = append(paths, path)
		}
	}
	var locks []*dataLock
	for _, path := range paths {
		lock, err := acquireDataLock(path)
		if err != nil {
			closeInitializationLocks(locks)
			return nil, err
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func closeInitializationLocks(locks []*dataLock) {
	for index := len(locks) - 1; index >= 0; index-- {
		_ = locks[index].Close()
	}
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
		case "--mode", "--listen", "--public-url", "--allowed-control-origin", "--tls-termination", "--acme-environment", "--acme-email":
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
			case "--tls-termination":
				result.tlsTermination = value
			case "--acme-environment":
				result.acmeEnvironment = value
			case "--acme-email":
				result.acmeEmail = value
			}
		case "--acme-accept-terms":
			result.acceptTerms = true
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
	if options.mode, err = read("Mode (local/lan-managed/public-ip-acme/external)", options.mode); err != nil {
		return err
	}
	if options.listen, err = read("Listen address", options.listen); err != nil {
		return err
	}
	if options.mode != "local" && options.mode != "loopback" {
		if options.publicURL, err = read("Public HTTPS URL", options.publicURL); err != nil {
			return err
		}
	}
	if options.mode == "external" || options.mode == "lan" {
		if options.tlsTermination, err = read("TLS termination (server/proxy)", options.tlsTermination); err != nil {
			return err
		}
	}
	if (options.mode == "external" || options.mode == "lan") && options.tlsTermination != "proxy" {
		if options.certFile, err = read("TLS certificate absolute path", options.certFile); err != nil {
			return err
		}
		if options.keyFile, err = read("TLS key absolute path", options.keyFile); err != nil {
			return err
		}
	}
	return nil
}

func optionsFromConfig(value ConfigFile) Options {
	return Options{
		DataDir: value.DataDir, Listen: value.Listen, DeploymentMode: value.DeploymentMode, PublicURL: value.PublicURL,
		TLSCertFile: value.TLS.CertFile, TLSKeyFile: value.TLS.KeyFile, TLSTermination: value.TLS.Termination, ACME: value.ACME,
		AllowedControlOrigins: append([]string(nil), value.AllowedControlOrigins...),
	}
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
