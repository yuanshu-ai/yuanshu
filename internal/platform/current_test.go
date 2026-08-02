package platform

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCurrentPlatformFailsClosed(t *testing.T) {
	current := Current()
	if current.Family() != expectedCurrentFamily {
		t.Fatalf("Family() = %q, want %q", current.Family(), expectedCurrentFamily)
	}

	wantSecureStore := expectedCurrentFamily == FamilyWindows
	if current.SecureStore().Available() != wantSecureStore {
		t.Fatalf("SecureStore().Available() = %v, want %v", current.SecureStore().Available(), wantSecureStore)
	}
	if current.Workspaces().Available() != expectedWorkspaceAvailable {
		t.Fatalf("Workspaces().Available() = %v, want %v", current.Workspaces().Available(), expectedWorkspaceAvailable)
	}
	if current.Processes().Available() != expectedProcessAvailable {
		t.Fatalf("Processes().Available() = %v, want %v", current.Processes().Available(), expectedProcessAvailable)
	}
	wantWindowsUserCapabilities := expectedCurrentFamily == FamilyWindows
	if current.IPC().Available() != expectedIPCAvailable {
		t.Fatalf("IPC().Available() = %v, want %v", current.IPC().Available(), expectedIPCAvailable)
	}
	if current.Autostart().Available() != wantWindowsUserCapabilities {
		t.Fatalf("Autostart().Available() = %v, want %v", current.Autostart().Available(), wantWindowsUserCapabilities)
	}

	const canary = "platform-sensitive-canary"
	tests := []struct {
		name      string
		available bool
		call      func() error
	}{}
	if !expectedIPCAvailable {
		tests = append(tests, struct {
			name      string
			available bool
			call      func() error
		}{"ipc listen", current.IPC().Available(), func() error {
			_, err := current.IPC().Listen(context.Background(), IPCName(canary))
			return err
		}}, struct {
			name      string
			available bool
			call      func() error
		}{"ipc dial", current.IPC().Available(), func() error {
			_, err := current.IPC().Dial(context.Background(), IPCName(canary))
			return err
		}})
	}
	if !wantWindowsUserCapabilities {
		tests = append(tests, struct {
			name      string
			available bool
			call      func() error
		}{"autostart install", current.Autostart().Available(), func() error {
			return current.Autostart().Install(context.Background(), AutostartEntry{
				ID: canary, Executable: canary, Env: []string{"SECRET=" + canary},
			})
		}}, struct {
			name      string
			available bool
			call      func() error
		}{"autostart remove", current.Autostart().Available(), func() error {
			return current.Autostart().Remove(context.Background(), canary)
		}}, struct {
			name      string
			available bool
			call      func() error
		}{"autostart status", current.Autostart().Available(), func() error {
			_, err := current.Autostart().Status(context.Background(), canary)
			return err
		}})
	}
	if !expectedProcessAvailable {
		tests = append(tests, struct {
			name      string
			available bool
			call      func() error
		}{"process", current.Processes().Available(), func() error {
			_, err := current.Processes().Start(context.Background(), ProcessSpec{
				Executable: canary,
				Args:       []string{canary},
				Env:        []string{"SECRET=" + canary},
				Directory:  canary,
			})
			return err
		}})
	}
	if !expectedWorkspaceAvailable {
		tests = append(tests, struct {
			name      string
			available bool
			call      func() error
		}{"workspace", current.Workspaces().Available(), func() error {
			_, err := current.Workspaces().Inspect(context.Background(), canary)
			return err
		}})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.available {
				t.Fatal("unimplemented production capability reported itself available")
			}
			err := test.call()
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatal("error exposed a sensitive argument")
			}
		})
	}
}
