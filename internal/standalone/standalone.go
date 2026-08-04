// Package standalone composes the formal Server and local Node roles without
// collapsing their trust or persistence boundaries.
package standalone

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/builtin"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	nodestore "github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	"github.com/yuanshu-ai/yuanshu/internal/server"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

var (
	ErrUsage       = errors.New("standalone command arguments are invalid")
	ErrUnavailable = errors.New("standalone runtime is unavailable")
)

const Usage = `Usage:
  yuanshu standalone [run] --data-dir <absolute-path> --config <absolute-path> [--listen <ip:port>]
    [--server-config <absolute-path>]
    [--public-url https://host[:port] --tls-cert <absolute-path> --tls-key <absolute-path>]
    [--allowed-control-origin https://web-host[:port]]
    [--master-key-file <absolute-path>]
    [--web | --no-web]
`

type Options struct {
	DataDir               string
	Config                string
	ServerConfig          string
	Listen                string
	DeploymentMode        server.DeploymentMode
	PublicURL             string
	TLSCertFile           string
	TLSKeyFile            string
	TLSTermination        string
	ACME                  server.ACMEConfig
	CertificateDataDir    string
	AllowedControlOrigins []string
	WebEnabled            *bool
	MasterKeyFile         string
	Stdout                io.Writer
	Platform              platform.Platform
	Random                io.Reader
	Clock                 func() time.Time
}

// Command runs the formal combined Server and local Node entry point.
func Command(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = io.WriteString(stdout, Usage)
		return nil
	}
	options, err := parseArguments(args)
	if err != nil {
		return err
	}
	options.Stdout = stdout
	return Run(ctx, options)
}

func parseArguments(args []string) (Options, error) {
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	} else if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		return Options{}, ErrUsage
	}
	options := Options{Listen: server.DefaultListenAddress}
	seen := make(map[string]bool)
	var origins []string
	var webOverride *bool
	for index := 0; index < len(args); index++ {
		name := args[index]
		if seen[name] && name != "--allowed-control-origin" {
			return Options{}, ErrUsage
		}
		seen[name] = true
		switch name {
		case "--data-dir":
			index++
			if index >= len(args) || options.DataDir != "" || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.DataDir = filepath.Clean(args[index])
		case "--config":
			index++
			if index >= len(args) || options.Config != "" || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.Config = filepath.Clean(args[index])
		case "--server-config":
			index++
			if index >= len(args) || options.ServerConfig != "" || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.ServerConfig = filepath.Clean(args[index])
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
		case "--master-key-file":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			options.MasterKeyFile = filepath.Clean(args[index])
		case "--allowed-control-origin":
			index++
			if index >= len(args) || !validControlOrigin(args[index]) {
				return Options{}, ErrUsage
			}
			origins = append(origins, strings.TrimSuffix(args[index], "/"))
		case "--web":
			if webOverride != nil {
				return Options{}, ErrUsage
			}
			value := true
			webOverride = &value
		case "--no-web":
			if webOverride != nil {
				return Options{}, ErrUsage
			}
			value := false
			webOverride = &value
		default:
			return Options{}, ErrUsage
		}
	}
	options.AllowedControlOrigins = origins
	options.WebEnabled = webOverride
	if options.DataDir == "" || options.Config == "" || !validListen(options.Listen) || !validPublicOptions(options) {
		return Options{}, ErrUsage
	}
	return options, nil
}

func validControlOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "http" && (host == "127.0.0.1" || host == "::1")
}

func validListen(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || net.ParseIP(host) == nil {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func validPublicOptions(options Options) bool {
	if options.DeploymentMode != "" {
		return server.ValidateConfigFile(server.ConfigFile{
			ConfigVersion: server.CurrentConfigVersion, DeploymentMode: options.DeploymentMode,
			DataDir: options.DataDir, Listen: options.Listen, PublicURL: options.PublicURL,
			AllowedControlOrigins: options.AllowedControlOrigins,
			TLS:                   server.TLSFileConfig{Termination: options.TLSTermination, CertFile: options.TLSCertFile, KeyFile: options.TLSKeyFile}, ACME: options.ACME,
		}) == nil
	}
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
	if err != nil || host != "127.0.0.1" && host != "::1" && tlsCount != 3 {
		return false
	}
	if options.PublicURL == "" {
		return true
	}
	parsed, err := url.Parse(options.PublicURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

// Run owns one formal Server, one local Node control session, and their
// in-process StandaloneTransport. Server and Node databases stay separate.
func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var platformCloser io.Closer
	if options.Platform == nil {
		configured, closer, err := defaultStandalonePlatform(options.DataDir, options.MasterKeyFile)
		if err != nil {
			return errors.Join(ErrUnavailable, errors.New("standalone platform is unavailable"))
		}
		options.Platform, platformCloser = configured, closer
		if platformCloser != nil {
			defer platformCloser.Close()
		}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Listen == "" {
		options.Listen = server.DefaultListenAddress
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.ServerConfig != "" {
		serverConfig, configErr := server.LoadConfigFile(options.ServerConfig)
		if configErr != nil {
			return errors.Join(ErrUnavailable, errors.New("server configuration is unavailable"))
		}
		if options.Listen == server.DefaultListenAddress && serverConfig.Listen != "" {
			options.Listen = serverConfig.Listen
		}
		if options.PublicURL == "" {
			options.PublicURL = serverConfig.PublicURL
		}
		if options.DeploymentMode == "" {
			options.DeploymentMode = serverConfig.DeploymentMode
		}
		if options.TLSCertFile == "" {
			options.TLSCertFile = serverConfig.TLS.CertFile
		}
		if options.TLSKeyFile == "" {
			options.TLSKeyFile = serverConfig.TLS.KeyFile
		}
		if options.TLSTermination == "" {
			options.TLSTermination = serverConfig.TLS.Termination
		}
		if options.ACME.Environment == "" {
			options.ACME = serverConfig.ACME
		}
		options.CertificateDataDir = serverConfig.DataDir
		if len(options.AllowedControlOrigins) == 0 {
			options.AllowedControlOrigins = append([]string(nil), serverConfig.AllowedControlOrigins...)
		}
		if options.WebEnabled == nil {
			options.WebEnabled = copyBool(serverConfig.Web.Enabled)
		}
	}
	if options.DataDir == "" || options.Config == "" || !filepath.IsAbs(options.DataDir) || !filepath.IsAbs(options.Config) || !validListen(options.Listen) || !validPublicOptions(options) || !validControlOrigins(options.AllowedControlOrigins) || options.Platform == nil {
		return ErrUsage
	}

	configurationStore, err := config.NewFileStore(options.Config)
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("configuration is unavailable"))
	}
	loaded, err := configurationStore.Load(ctx)
	if err != nil || loaded.Config.Transport.Mode != config.TransportStandalone {
		return errors.Join(ErrUnavailable, errors.New("standalone configuration is invalid"))
	}

	serverDir := filepath.Join(options.DataDir, "server")
	nodeDir := filepath.Join(options.DataDir, "node")
	if err := prepareDirectory(options.DataDir); err != nil {
		return err
	}
	if err := prepareDirectory(serverDir); err != nil {
		return err
	}
	if err := prepareDirectory(nodeDir); err != nil {
		return err
	}

	local, err := nodestore.Open(ctx, filepath.Join(nodeDir, "node.db"), nodestore.Options{Clock: options.Clock})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("node database is unavailable"))
	}
	defer local.Close()
	workspaceManager, err := workspace.NewManager(options.Platform.Workspaces(), local)
	if err != nil || workspaceManager.Reconcile(ctx, loaded.Config.Workspaces) != nil {
		return errors.Join(ErrUnavailable, errors.New("workspace configuration is unavailable"))
	}
	identityManager, err := identity.NewManager(local, options.Platform.SecureStore(), loaded.Config.Identity.PrivateKeyRef, identity.Options{Random: options.Random, Clock: options.Clock})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("node identity is unavailable"))
	}
	nodeIdentity, err := identityManager.Ensure(ctx)
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("node identity is unavailable"))
	}
	nodeIdentity, err = ensureServerBinding(ctx, serverDir, loaded.Config.Host.Name, options.Platform.Family(), nodeIdentity, identityManager, options.Random, options.Clock)
	if err != nil {
		return err
	}
	sessionToken := make([]byte, 32)
	if _, err := io.ReadFull(options.Random, sessionToken); err != nil {
		return errors.Join(ErrUnavailable, errors.New("standalone session generation failed"))
	}
	defer clear(sessionToken)
	sessionExpiresAt := options.Clock().UTC().Add(15 * time.Minute)

	registry, err := builtin.NewRegistry(builtin.Options{
		CodexConfig: loaded.Config.Adapters.Codex, Processes: options.Platform.Processes(),
		Inspector:  options.Platform.ProcessInspector(),
		Workspaces: workspaceManager, Threads: local,
	})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("Codex adapter is unavailable"))
	}
	handle, err := registry.CreateDefault()
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("Codex adapter is unavailable"))
	}
	runtime, err := handle.Adapter.StartRuntime(ctx)
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("Codex runtime is unavailable"))
	}
	defer closeRuntime(runtime)
	events, err := eventlog.NewManager(local, eventlog.Options{
		OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID,
		MaxAge:   time.Duration(loaded.Config.Events.MaxAgeHours) * time.Hour,
		MaxBytes: int64(loaded.Config.Events.MaxSizeMiB) << 20,
		Clock:    options.Clock, Random: options.Random,
	})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("node event log is unavailable"))
	}
	validator, err := protocol.NewValidator(protocol.Options{TrustStore: local, ReplayStore: local, Now: options.Clock})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("control validator is unavailable"))
	}
	serverSide, nodeSide, err := transport.NewStandalonePair(transport.StandaloneOptions{})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("standalone transport is unavailable"))
	}
	defer serverSide.Close()
	defer nodeSide.Close()
	session, err := node.NewControlSession(node.ControlSessionOptions{
		Transport: nodeSide, Validator: validator, Target: protocol.Target{OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID},
		Events: events, Store: local, Runtime: runtime, DeviceName: loaded.Config.Host.Name,
	})
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var localManagement *node.StandaloneManagement
	if options.PublicURL != "" {
		relayURL := "wss" + strings.TrimPrefix(strings.TrimSuffix(options.PublicURL, "/"), "https") + "/node/connect"
		localManagement, err = node.StartStandaloneManagement(runCtx, node.StandaloneManagementOptions{
			IPC: options.Platform.IPC(), RelayURL: relayURL, Identity: nodeIdentity, Signer: identityManager, Local: local,
			SessionToken: sessionToken, SessionExpiresAt: sessionExpiresAt, Stop: cancel,
		})
		if err != nil {
			return errors.Join(ErrUnavailable, errors.New("standalone local management is unavailable"))
		}
		defer localManagement.Close()
	}
	results := make(chan error, 2)
	go func() { results <- session.Run(runCtx) }()
	go func() {
		results <- server.Run(runCtx, server.Options{
			DataDir: serverDir, Listen: options.Listen, DeploymentMode: options.DeploymentMode, PublicURL: options.PublicURL,
			CertificateDataDir: options.CertificateDataDir,
			TLSCertFile:        options.TLSCertFile, TLSKeyFile: options.TLSKeyFile, TLSTermination: options.TLSTermination, ACME: options.ACME,
			AllowedControlOrigins: options.AllowedControlOrigins,
			WebEnabled:            options.WebEnabled, Stdout: options.Stdout, Random: options.Random, Clock: options.Clock,
			LocalNode: &server.LocalNodeSession{OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID, Transport: serverSide, SessionToken: sessionToken, SessionExpiresAt: sessionExpiresAt},
		})
	}()
	first := <-results
	cancel()
	_ = serverSide.Close()
	_ = nodeSide.Close()
	second := <-results
	if ctx.Err() != nil {
		return nil
	}
	if first != nil {
		return first
	}
	return second
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validControlOrigins(values []string) bool {
	for _, value := range values {
		if !validControlOrigin(value) {
			return false
		}
	}
	return true
}

func ensureServerBinding(ctx context.Context, serverDir, name string, family platform.Family, current identity.Identity, manager *identity.Manager, random io.Reader, clock func() time.Time) (identity.Identity, error) {
	database, err := serverstore.Open(ctx, filepath.Join(serverDir, "server.db"), serverstore.Options{Clock: clock})
	if err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("server database is unavailable"))
	}
	defer database.Close()
	if current.OwnerID != "" || current.NodeID != "" {
		record, err := database.NodeSession(ctx, current.NodeID)
		if err != nil || record.OwnerID != current.OwnerID || !equalPublicKeys(record.PublicKey, current.PublicKey) || record.Status != "active" {
			return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone identity binding does not match server metadata"))
		}
		return current, nil
	}
	service, err := server.NewBootstrapService(database, server.BootstrapOptions{Random: random, Clock: clock})
	if err != nil {
		return identity.Identity{}, ErrUnavailable
	}
	secret, issued, err := service.Rotate(ctx)
	if err != nil || !issued {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone server is already claimed"))
	}
	requestID := make([]byte, 16)
	if _, err := io.ReadFull(random, requestID); err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone enrollment failed"))
	}
	response, _, err := service.Claim(ctx, secret, server.ClaimRequest{
		RequestID: "req_" + base64.RawURLEncoding.EncodeToString(requestID), Name: name, OS: string(family), Version: "dev",
		PublicKey: base64.RawURLEncoding.EncodeToString(current.PublicKey),
	})
	if err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone enrollment failed"))
	}
	bound, err := manager.Bind(ctx, response.OwnerID, response.NodeID)
	if err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone identity binding failed"))
	}
	return bound, nil
}

func equalPublicKeys(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func prepareDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.Join(ErrUnavailable, errors.New("standalone data directory is unavailable"))
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(ErrUnavailable, errors.New("standalone data directory is unavailable"))
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.Join(ErrUnavailable, errors.New("standalone data directory permissions are unavailable"))
	}
	return nil
}

func closeRuntime(runtime adapter.Runtime) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = runtime.Close(ctx)
}
