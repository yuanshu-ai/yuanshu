//go:build windows

package platform

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWindowsNamedPipeIsCurrentUserOnlyAndDuplex(t *testing.T) {
	ipc := newWindowsLocalIPC()
	name := IPCName("test-" + strings.ReplaceAll(t.Name(), "/", "-"))
	path, descriptor, err := windowsPipeIdentity(name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(descriptor, ";;;WD)") || !strings.Contains(descriptor, ";;;SY)") || !strings.HasPrefix(path, `\\.\pipe\yuanshu-`) {
		t.Fatalf("unsafe pipe identity")
	}
	listener, err := ipc.Listen(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := ipc.Listen(context.Background(), name); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Listen error = %v", err)
	}
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer connection.Close()
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(connection, buffer); err == nil && string(buffer) == "ping" {
			_, err = connection.Write([]byte("pong"))
		}
		accepted <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := ipc.Dial(ctx, name)
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
}

func TestWindowsNamedPipeRejectsInvalidLogicalNames(t *testing.T) {
	ipc := newWindowsLocalIPC()
	for _, name := range []IPCName{"", `..\escape`, "has space", "line\nbreak"} {
		if _, err := ipc.Listen(context.Background(), name); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Listen(%q) error = %v", name, err)
		}
	}
}
