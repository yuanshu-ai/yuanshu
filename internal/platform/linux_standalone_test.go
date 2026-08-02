//go:build linux

package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxEncryptedStoreRoundTripAndTamper(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "master.key")
	key := bytes.Repeat([]byte{0x41}, 32)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newLinuxEncryptedStore(filepath.Join(root, "secrets"), keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ref := SecretRef("synthetic/ref")
	secret := []byte("plaintext-canary")
	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatal(err)
	}
	secret[0] = 'X'
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plaintext-canary" {
		t.Fatalf("Get() = %q", got)
	}
	got[0] = 'X'
	again, err := store.Get(context.Background(), ref)
	if err != nil || string(again) != "plaintext-canary" {
		t.Fatalf("second Get() = %q, %v", again, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "secrets"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("secret entries = %d, %v", len(entries), err)
	}
	ciphertextPath := filepath.Join(root, "secrets", entries[0].Name())
	ciphertext, err := os.ReadFile(ciphertextPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("plaintext-canary")) {
		t.Fatal("ciphertext contains plaintext")
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	if err := os.WriteFile(ciphertextPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); err == nil || strings.Contains(err.Error(), "plaintext-canary") {
		t.Fatalf("tampered Get() error = %v", err)
	}
}

func TestLinuxEncryptedStoreRejectsUnsafeMasterKey(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "master.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{1}, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newLinuxEncryptedStore(filepath.Join(root, "secrets"), keyPath); err == nil {
		t.Fatal("unsafe master-key permissions were accepted")
	}
}

func TestLinuxLocalIPCRoundTripAndSingleInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	ipc := newLinuxLocalIPC()
	listener, err := ipc.Listen(context.Background(), IPCName("standalone-test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(connection, buffer); err == nil && string(buffer) == "ping" {
			_, err = connection.Write([]byte("pong"))
		}
		done <- err
	}()
	connection, err := ipc.Dial(context.Background(), IPCName("standalone-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil || string(response) != "pong" {
		t.Fatalf("response = %q, %v", response, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Listen(context.Background(), IPCName("standalone-test")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second Listen() = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err := ipc.Dial(context.Background(), IPCName("standalone-test")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Dial after Close() = %v", err)
	}
}

func TestLinuxWorkspaceInspectorReportsLinksAndProtectedPaths(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	inspector := newLinuxWorkspaceInspector()
	facts, err := inspector.Inspect(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.CrossesLinkBoundary || facts.CanonicalPath != real || !facts.IsDirectory {
		t.Fatalf("facts = %+v", facts)
	}
	rootFacts, err := inspector.Inspect(context.Background(), "/")
	if err != nil || !rootFacts.IsFilesystemRoot {
		t.Fatalf("root facts = %+v, %v", rootFacts, err)
	}
	etcFacts, err := inspector.Inspect(context.Background(), "/etc")
	if err != nil || !etcFacts.IsSystem {
		t.Fatalf("system facts = %+v, %v", etcFacts, err)
	}
}

func TestLinuxProcessManagerStdioWaitAndStop(t *testing.T) {
	manager := newLinuxProcessManager()
	process, err := manager.Start(context.Background(), ProcessSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf output; printf error >&2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(process.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(process.Stderr())
	if err != nil {
		t.Fatal(err)
	}
	exit, err := process.Wait(context.Background())
	if err != nil || exit.Code != 0 || exit.Forced || string(stdout) != "output" || string(stderr) != "error" {
		t.Fatalf("exit=%+v stdout=%q stderr=%q err=%v", exit, stdout, stderr, err)
	}

	process, err = manager.Start(context.Background(), ProcessSpec{Executable: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exit, err = process.Stop(stopContext)
	if err != nil || !exit.Forced {
		t.Fatalf("Stop() = %+v, %v", exit, err)
	}
}

func TestLinuxIPCInvalidNameDoesNotExposeIt(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	canary := IPCName("../sensitive-socket")
	_, err := newLinuxLocalIPC().Dial(context.Background(), canary)
	if !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), string(canary)) {
		t.Fatalf("Dial() error = %v", err)
	}
}

var _ net.Listener = (*linuxIPCListener)(nil)
