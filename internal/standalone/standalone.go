// Package standalone composes the formal Server and local Node roles without
// collapsing their trust or persistence boundaries.
package standalone

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
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
  yuanshu standalone [run] --data-dir <absolute-path> --config <absolute-path> [--listen 127.0.0.1:7444]
`

type Options struct {
	DataDir  string
	Config   string
	Listen   string
	Stdout   io.Writer
	Platform platform.Platform
	Random   io.Reader
	Clock    func() time.Time
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
	options := Options{Listen: "127.0.0.1:7444"}
	listenSet := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
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
		case "--listen":
			index++
			if index >= len(args) || listenSet {
				return Options{}, ErrUsage
			}
			options.Listen = args[index]
			listenSet = true
		default:
			return Options{}, ErrUsage
		}
	}
	if options.DataDir == "" || options.Config == "" || !validListen(options.Listen) {
		return Options{}, ErrUsage
	}
	return options, nil
}

func validListen(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host != "127.0.0.1" && host != "::1" {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
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
	if options.Platform == nil {
		options.Platform = platform.Current()
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Listen == "" {
		options.Listen = "127.0.0.1:7444"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.DataDir == "" || options.Config == "" || !filepath.IsAbs(options.DataDir) || !filepath.IsAbs(options.Config) || !validListen(options.Listen) || options.Platform == nil {
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

	agent, err := codex.New(codex.Options{Config: loaded.Config.Adapters.Codex, Processes: options.Platform.Processes(), Workspaces: workspaceManager, Threads: local})
	if err != nil {
		return errors.Join(ErrUnavailable, errors.New("Codex adapter is unavailable"))
	}
	runtime, err := agent.StartRuntime(ctx)
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
	results := make(chan error, 2)
	go func() { results <- session.Run(runCtx) }()
	go func() {
		results <- server.Run(runCtx, server.Options{
			DataDir: serverDir, Listen: options.Listen, Stdout: options.Stdout, Random: options.Random, Clock: options.Clock,
			LocalNode: &server.LocalNodeSession{OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID, Transport: serverSide},
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

func ensureServerBinding(ctx context.Context, serverDir, name string, family platform.Family, current identity.Identity, manager *identity.Manager, random io.Reader, clock func() time.Time) (identity.Identity, error) {
	database, err := serverstore.Open(ctx, filepath.Join(serverDir, "server.db"), serverstore.Options{Clock: clock})
	if err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("server database is unavailable"))
	}
	defer database.Close()
	if current.OwnerID != "" || current.NodeID != "" {
		record, err := database.NodeSession(ctx, current.NodeID)
		if err != nil || record.OwnerID != current.OwnerID || !bytes.Equal(record.PublicKey, current.PublicKey) || record.Status != "active" {
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
	credential := make([]byte, 32)
	requestID := make([]byte, 16)
	if _, err := io.ReadFull(random, credential); err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone enrollment failed"))
	}
	defer clear(credential)
	if _, err := io.ReadFull(random, requestID); err != nil {
		return identity.Identity{}, errors.Join(ErrUnavailable, errors.New("standalone enrollment failed"))
	}
	digest := sha256.Sum256(credential)
	response, _, err := service.Claim(ctx, secret, server.ClaimRequest{
		RequestID: "req_" + base64.RawURLEncoding.EncodeToString(requestID), Name: name, OS: string(family), Version: "dev",
		PublicKey: base64.RawURLEncoding.EncodeToString(current.PublicKey), CredentialHash: base64.RawURLEncoding.EncodeToString(digest[:]),
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
