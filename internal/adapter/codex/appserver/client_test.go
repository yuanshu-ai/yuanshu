package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestClientInitializeCallNotificationAndRequest(t *testing.T) {
	manager := fake.NewProcessManager()
	client, err := Start(context.Background(), Options{
		Processes: manager,
		Spec:      platform.ProcessSpec{Executable: "synthetic-codex", Args: []string{"app-server", "--stdio"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	process := manager.LastProcess()
	go serveSyntheticAppServer(process)

	result, err := client.Initialize(context.Background(), ClientInfo{Name: "yuanshu", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserAgent != "codex_cli_rs/0.144.6" {
		t.Fatalf("initialize result = %#v", result)
	}
	var list struct {
		Data []any `json:"data"`
	}
	if err := client.Call(context.Background(), "thread/list", map[string]any{"limit": 1}, &list); err != nil {
		t.Fatal(err)
	}
	if list.Data == nil {
		t.Fatal("thread/list data is nil")
	}

	var request Message
	select {
	case request = <-client.Messages():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server request")
	}
	if !request.IsRequest() || request.Method != "item/commandExecution/requestApproval" {
		t.Fatalf("message = %#v", request)
	}
	if err := client.Respond(*request.ID, map[string]string{"decision": "decline"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientCancellationMalformedMessageAndQueueOverflow(t *testing.T) {
	t.Run("cancel call", func(t *testing.T) {
		manager := fake.NewProcessManager()
		client, err := Start(context.Background(), Options{Processes: manager, Spec: platform.ProcessSpec{Executable: "synthetic"}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := client.Call(ctx, "thread/list", struct{}{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		go drainAndComplete(manager.LastProcess())
		_ = client.Close()
	})

	for _, test := range []struct {
		name  string
		lines string
		want  error
	}{
		{"malformed", "not-json\n", ErrInvalidMessage},
		{"overflow", "{\"method\":\"one\",\"params\":{}}\n{\"method\":\"two\",\"params\":{}}\n", ErrQueueFull},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := fake.NewProcessManager()
			client, err := Start(context.Background(), Options{Processes: manager, Spec: platform.ProcessSpec{Executable: "synthetic"}, QueueSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			process := manager.LastProcess()
			go func() {
				_ = process.WriteStdout([]byte(test.lines))
				_ = process.Complete(0)
			}()
			select {
			case <-client.Done():
			case <-time.After(time.Second):
				t.Fatal("client did not terminate")
			}
			if !errors.Is(client.Err(), test.want) {
				t.Fatalf("error = %v, want %v", client.Err(), test.want)
			}
		})
	}
}

func TestClientErrorsDoNotContainPayload(t *testing.T) {
	manager := fake.NewProcessManager()
	client, err := Start(context.Background(), Options{Processes: manager, Spec: platform.ProcessSpec{Executable: "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "private-payload-canary"
	process := manager.LastProcess()
	go func() {
		scanner := bufio.NewScanner(process.Input())
		if scanner.Scan() {
			var request struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &request)
			_ = process.WriteStdout([]byte(fmt.Sprintf("{\"id\":%d,\"error\":{\"code\":-1,\"message\":%q}}\n", request.ID, canary)))
		}
		drainAndComplete(process)
	}()
	err = client.Call(context.Background(), "synthetic", map[string]string{"value": canary}, nil)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error = %v", err)
	}
	_ = client.Close()
}

func TestClientForcedCloseTreatsPipeShutdownAsExpected(t *testing.T) {
	manager := fake.NewProcessManager()
	client, err := Start(context.Background(), Options{
		Processes:    manager,
		Spec:         platform.ProcessSpec{Executable: "synthetic"},
		CloseTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("forced Close error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("repeated Close error = %v", err)
	}
}

func serveSyntheticAppServer(process *fake.Process) {
	scanner := bufio.NewScanner(process.Input())
	initialized := false
	for scanner.Scan() {
		var request struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = process.WriteStdout([]byte(fmt.Sprintf("{\"id\":%d,\"result\":{\"userAgent\":\"codex_cli_rs/0.144.6\"}}\n", *request.ID)))
		case "initialized":
			initialized = true
		case "thread/list":
			if initialized {
				_ = process.WriteStdout([]byte(fmt.Sprintf("{\"id\":%d,\"result\":{\"data\":[]}}\n", *request.ID)))
				_ = process.WriteStdout([]byte("{\"id\":\"approval-string\",\"method\":\"item/commandExecution/requestApproval\",\"params\":{}}\n"))
			}
		}
	}
	_ = process.Complete(0)
}

func drainAndComplete(process *fake.Process) {
	_, _ = bufio.NewReader(process.Input()).ReadString(0)
	_ = process.Complete(0)
}
