package platform_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestFakePlatformAggregate(t *testing.T) {
	for _, family := range []platformpkg.Family{
		platformpkg.FamilyWindows,
		platformpkg.FamilyDarwin,
		platformpkg.FamilyLinux,
	} {
		instance, err := fake.New(family)
		if err != nil {
			t.Fatal(err)
		}
		if instance.Family() != family {
			t.Fatalf("Family() = %q, want %q", instance.Family(), family)
		}
		if !instance.SecureStore().Available() || !instance.Processes().Available() ||
			!instance.IPC().Available() || !instance.Autostart().Available() ||
			!instance.Workspaces().Available() {
			t.Fatal("fake capability unexpectedly unavailable")
		}
	}
	if _, err := fake.New(platformpkg.Family("other")); !errors.Is(err, platformpkg.ErrInvalidArgument) {
		t.Fatalf("unknown family error = %v", err)
	}
}

func TestSecureStoreContract(t *testing.T) {
	store := fake.NewSecureStore()
	ctx := context.Background()
	ref := platformpkg.SecretRef("synthetic-secret-ref")
	input := []byte("synthetic-secret")
	if err := store.Put(ctx, ref, input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	first, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "synthetic-secret" {
		t.Fatal("Put did not copy input bytes")
	}
	first[0] = 'X'
	second, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "synthetic-secret" {
		t.Fatal("Get did not copy output bytes")
	}
	if err := store.Put(ctx, ref, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(ctx, ref); string(got) != "replacement" {
		t.Fatal("Put did not replace an existing value")
	}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, ref); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("Get after Delete error = %v", err)
	}
	if err := store.Delete(ctx, ref); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("second Delete error = %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(canceled, ref, []byte("ignored")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Put error = %v", err)
	}

	const writers = 32
	var wg sync.WaitGroup
	for index := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localRef := platformpkg.SecretRef(string(rune('a' + index)))
			if err := store.Put(ctx, localRef, []byte{byte(index)}); err != nil {
				t.Errorf("concurrent Put: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestProcessContract(t *testing.T) {
	manager := fake.NewProcessManager()
	ctx := context.Background()
	spec := platformpkg.ProcessSpec{
		Executable: "synthetic-executable",
		Args:       []string{"one", "two"},
		Env:        []string{"SYNTHETIC=value"},
		Directory:  "synthetic-directory",
	}
	process, err := manager.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Args[0] = "mutated"
	spec.Env[0] = "MUTATED=value"
	started := manager.Started()
	if started[0].Args[0] != "one" || started[0].Env[0] != "SYNTHETIC=value" {
		t.Fatal("Start did not deep-copy ProcessSpec")
	}
	started[0].Args[0] = "mutated-again"
	if manager.Started()[0].Args[0] != "one" {
		t.Fatal("Started did not deep-copy ProcessSpec output")
	}

	stdinResult := make(chan error, 1)
	go func() {
		_, writeErr := process.Stdin().Write([]byte("input"))
		stdinResult <- writeErr
	}()
	input := make([]byte, len("input"))
	if _, err := io.ReadFull(manager.LastProcess().Input(), input); err != nil {
		t.Fatal(err)
	}
	if err := <-stdinResult; err != nil || string(input) != "input" {
		t.Fatalf("stdin = %q, error = %v", input, err)
	}

	stdoutResult := make(chan error, 1)
	go func() { stdoutResult <- manager.LastProcess().WriteStdout([]byte("stdout")) }()
	stdout := make([]byte, len("stdout"))
	if _, err := io.ReadFull(process.Stdout(), stdout); err != nil {
		t.Fatal(err)
	}
	if err := <-stdoutResult; err != nil || string(stdout) != "stdout" {
		t.Fatalf("stdout = %q, error = %v", stdout, err)
	}

	stderrResult := make(chan error, 1)
	go func() { stderrResult <- manager.LastProcess().WriteStderr([]byte("stderr")) }()
	stderr := make([]byte, len("stderr"))
	if _, err := io.ReadFull(process.Stderr(), stderr); err != nil {
		t.Fatal(err)
	}
	if err := <-stderrResult; err != nil || string(stderr) != "stderr" {
		t.Fatalf("stderr = %q, error = %v", stderr, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := process.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait timeout error = %v", err)
	}
	if err := manager.LastProcess().Complete(7); err != nil {
		t.Fatal(err)
	}
	exit, err := process.Wait(ctx)
	if err != nil || exit != (platformpkg.ProcessExit{Code: 7}) {
		t.Fatalf("natural exit = %+v, error = %v", exit, err)
	}
	if err := manager.LastProcess().Complete(8); !errors.Is(err, platformpkg.ErrProcessStopped) {
		t.Fatalf("second Complete error = %v", err)
	}

	stopProcess, err := manager.Start(ctx, platformpkg.ProcessSpec{Executable: "synthetic-stop"})
	if err != nil {
		t.Fatal(err)
	}
	forced, err := stopProcess.Stop(ctx)
	if err != nil || !forced.Forced {
		t.Fatalf("forced exit = %+v, error = %v", forced, err)
	}
	again, err := stopProcess.Stop(ctx)
	if err != nil || again != forced {
		t.Fatalf("idempotent Stop = %+v, error = %v", again, err)
	}
	if _, err := stopProcess.Stdin().Write([]byte("after-stop")); !errors.Is(err, platformpkg.ErrProcessStopped) {
		t.Fatalf("stdin after Stop error = %v", err)
	}

	inherit, err := manager.Start(ctx, platformpkg.ProcessSpec{Executable: "inherit", Env: nil})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := manager.Start(ctx, platformpkg.ProcessSpec{Executable: "replace-empty", Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if manager.Started()[2].Env != nil || manager.Started()[3].Env == nil {
		t.Fatal("nil and non-nil environment semantics were not preserved")
	}
	_, _ = inherit.Stop(ctx)
	_, _ = empty.Stop(ctx)
}

func TestIPCContract(t *testing.T) {
	ipc := fake.NewLocalIPC()
	ctx := context.Background()
	name := platformpkg.IPCName("synthetic-endpoint")
	listener, err := ipc.Listen(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := ipc.Listen(ctx, name); !errors.Is(err, platformpkg.ErrAlreadyExists) {
		t.Fatalf("duplicate Listen error = %v", err)
	}
	client, err := ipc.Dial(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	assertConnectionMessage(t, client, server, "client-to-server")
	assertConnectionMessage(t, server, client, "server-to-client")
	if listener.Addr().String() == string(name) {
		t.Fatal("logical IPC name leaked through net.Addr")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close error = %v", err)
	}
	if _, err := ipc.Dial(ctx, name); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("Dial after Close error = %v", err)
	}
	if _, err := ipc.Dial(ctx, platformpkg.IPCName("unknown")); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("unknown Dial error = %v", err)
	}

	blocked, err := ipc.Listen(ctx, platformpkg.IPCName("blocked-accept"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, acceptErr := blocked.Accept()
		result <- acceptErr
	}()
	if err := blocked.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("blocked Accept error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Accept")
	}
}

func TestAutostartContract(t *testing.T) {
	manager := fake.NewAutostartManager()
	ctx := context.Background()
	entry := platformpkg.AutostartEntry{
		ID: "synthetic-entry", Executable: "synthetic-executable",
		Args: []string{"node"}, Env: []string{"SYNTHETIC=value"}, Directory: "synthetic-directory",
	}
	if err := manager.Install(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.Args[0] = "mutated"
	status, err := manager.Status(ctx, entry.ID)
	if err != nil || !status.Installed || status.Entry.Args[0] != "node" {
		t.Fatalf("Status = %+v, error = %v", status, err)
	}
	status.Entry.Args[0] = "mutated-again"
	status, _ = manager.Status(ctx, entry.ID)
	if status.Entry.Args[0] != "node" {
		t.Fatal("Status did not copy output")
	}
	if err := manager.Install(ctx, status.Entry); err != nil {
		t.Fatalf("idempotent Install: %v", err)
	}
	replacement := status.Entry
	replacement.Executable = "replacement-executable"
	if err := manager.Install(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	status, _ = manager.Status(ctx, entry.ID)
	if status.Entry.Executable != "replacement-executable" {
		t.Fatal("changed Install did not replace the entry")
	}
	if err := manager.Remove(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(ctx, entry.ID)
	if err != nil || status.Installed {
		t.Fatalf("removed Status = %+v, error = %v", status, err)
	}
	if err := manager.Remove(ctx, entry.ID); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("second Remove error = %v", err)
	}
}

func TestWorkspaceInspectorReportsFactsWithoutPolicy(t *testing.T) {
	inspector := fake.NewWorkspaceInspector()
	ctx := context.Background()
	path := "synthetic-input-path"
	expected := platformpkg.WorkspaceFacts{
		CanonicalPath:          "synthetic-canonical-path",
		FilesystemRoot:         "synthetic-root",
		IsFilesystemRoot:       true,
		IsHome:                 true,
		IsSystem:               true,
		CrossesLinkBoundary:    true,
		CrossesReparseBoundary: true,
	}
	if err := inspector.Register(path, expected); err != nil {
		t.Fatal(err)
	}
	actual, err := inspector.Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("facts = %+v, want %+v", actual, expected)
	}
	if _, err := inspector.Inspect(ctx, "unknown-path"); !errors.Is(err, platformpkg.ErrNotFound) {
		t.Fatalf("unknown Inspect error = %v", err)
	}
}

func TestPersistentErrorsContextAndSanitization(t *testing.T) {
	const canary = "sensitive-platform-canary"
	injected := errors.New("synthetic injected failure")
	ctx := context.Background()

	store := fake.NewSecureStore()
	store.SetError(injected)
	assertPersistentError(t, func() error { return store.Put(ctx, platformpkg.SecretRef(canary), []byte(canary)) }, injected, canary)

	processes := fake.NewProcessManager()
	processes.SetError(injected)
	assertPersistentError(t, func() error {
		_, err := processes.Start(ctx, platformpkg.ProcessSpec{Executable: canary, Env: []string{"SECRET=" + canary}})
		return err
	}, injected, canary)

	ipc := fake.NewLocalIPC()
	ipc.SetError(injected)
	assertPersistentError(t, func() error {
		_, err := ipc.Listen(ctx, platformpkg.IPCName(canary))
		return err
	}, injected, canary)

	autostart := fake.NewAutostartManager()
	autostart.SetError(injected)
	assertPersistentError(t, func() error {
		return autostart.Install(ctx, platformpkg.AutostartEntry{ID: canary, Executable: canary})
	}, injected, canary)

	workspaces := fake.NewWorkspaceInspector()
	workspaces.SetError(injected)
	assertPersistentError(t, func() error {
		_, err := workspaces.Inspect(ctx, canary)
		return err
	}, injected, canary)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(canceled, platformpkg.SecretRef(canary), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation did not take priority: %v", err)
	}
	if _, err := processes.Start(canceled, platformpkg.ProcessSpec{Executable: canary}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Start error = %v", err)
	}
	if _, err := ipc.Dial(canceled, platformpkg.IPCName(canary)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Dial error = %v", err)
	}
	if _, err := autostart.Status(canceled, canary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Status error = %v", err)
	}
	if _, err := workspaces.Inspect(canceled, canary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Inspect error = %v", err)
	}
}

func TestFakeCapabilitiesAreConcurrent(t *testing.T) {
	ctx := context.Background()
	secrets := fake.NewSecureStore()
	processes := fake.NewProcessManager()
	ipc := fake.NewLocalIPC()
	autostart := fake.NewAutostartManager()
	workspaces := fake.NewWorkspaceInspector()

	const workers = 24
	var wg sync.WaitGroup
	for index := range workers {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("synthetic-%02d", index)

			if err := secrets.Put(ctx, platformpkg.SecretRef(id), []byte(id)); err != nil {
				t.Errorf("Put: %v", err)
			}
			if _, err := secrets.Get(ctx, platformpkg.SecretRef(id)); err != nil {
				t.Errorf("Get: %v", err)
			}

			started, err := processes.Start(ctx, platformpkg.ProcessSpec{Executable: id})
			if err != nil {
				t.Errorf("Start: %v", err)
			} else if _, err := started.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}

			listener, err := ipc.Listen(ctx, platformpkg.IPCName(id))
			if err != nil {
				t.Errorf("Listen: %v", err)
			} else {
				connection, dialErr := ipc.Dial(ctx, platformpkg.IPCName(id))
				if dialErr != nil {
					t.Errorf("Dial: %v", dialErr)
				} else {
					accepted, acceptErr := listener.Accept()
					if acceptErr != nil {
						t.Errorf("Accept: %v", acceptErr)
					} else {
						_ = accepted.Close()
					}
					_ = connection.Close()
				}
				_ = listener.Close()
			}

			entry := platformpkg.AutostartEntry{ID: id, Executable: id}
			if err := autostart.Install(ctx, entry); err != nil {
				t.Errorf("Install: %v", err)
			}
			if _, err := autostart.Status(ctx, id); err != nil {
				t.Errorf("Status: %v", err)
			}

			facts := platformpkg.WorkspaceFacts{CanonicalPath: id, FilesystemRoot: "synthetic-root"}
			if err := workspaces.Register(id, facts); err != nil {
				t.Errorf("Register: %v", err)
			}
			if _, err := workspaces.Inspect(ctx, id); err != nil {
				t.Errorf("Inspect: %v", err)
			}
		}()
	}
	wg.Wait()
}

func assertConnectionMessage(t *testing.T, writer net.Conn, reader net.Conn, message string) {
	t.Helper()
	writeResult := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte(message))
		writeResult <- err
	}()
	buffer := make([]byte, len(message))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if err := <-writeResult; err != nil {
		t.Fatal(err)
	}
	if string(buffer) != message {
		t.Fatalf("message = %q, want %q", buffer, message)
	}
}

func assertPersistentError(t *testing.T, call func() error, expected error, canary string) {
	t.Helper()
	for range 2 {
		err := call()
		if !errors.Is(err, expected) {
			t.Fatalf("error = %v, want injected error", err)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatal("error exposed a sensitive argument")
		}
	}
}
