//go:build darwin

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinUnixIPCIsPrivateDuplexAndCleansStaleSockets(t *testing.T) {
	name := IPCName("darwin-test")
	path, err := darwinSocketPath(name)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := newDarwinLocalIPC().Listen(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", info.Mode().Perm(), err)
	}
	directory, err := os.Stat(filepath.Dir(path))
	if err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode = %v, %v", directory.Mode().Perm(), err)
	}
	if _, err := newDarwinLocalIPC().Listen(context.Background(), name); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Listen() = %v", err)
	}

	accepted := make(chan error, 1)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				accepted <- acceptErr
				return
			}
			buffer := make([]byte, 4)
			_, acceptErr = io.ReadFull(connection, buffer)
			if acceptErr == nil && string(buffer) == "ping" {
				_, acceptErr = connection.Write([]byte("pong"))
				_ = connection.Close()
				accepted <- acceptErr
				return
			}
			_ = connection.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := newDarwinLocalIPC().Dial(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(connection, reply); err != nil || string(reply) != "pong" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after close = %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleListener, err := newDarwinLocalIPC().Listen(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	_ = staleListener.Close()
	if _, err := newDarwinLocalIPC().Dial(context.Background(), name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Dial() after close = %v", err)
	}
}

func TestDarwinUnixIPCRejectsUnsafeNames(t *testing.T) {
	ipc := newDarwinLocalIPC()
	for _, name := range []IPCName{"", "../escape", "has space", "line\nbreak"} {
		if _, err := ipc.Listen(context.Background(), name); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Listen(%q) = %v", name, err)
		}
	}
}
