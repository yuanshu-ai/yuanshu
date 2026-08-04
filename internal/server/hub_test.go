package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

type fakeSessionStore struct {
	node    serverstore.NodeSession
	control serverstore.ControlClientSession
}

func (s fakeSessionStore) NodeSession(_ context.Context, id string) (serverstore.NodeSession, error) {
	if id != s.node.NodeID {
		return serverstore.NodeSession{}, serverstore.ErrNotFound
	}
	return s.node, nil
}

func (s fakeSessionStore) ControlClientSession(_ context.Context, id string) (serverstore.ControlClientSession, error) {
	if id != s.control.ClientID {
		return serverstore.ControlClientSession{}, serverstore.ErrNotFound
	}
	return s.control, nil
}

type hubFixture struct {
	hub            *Hub
	server         *httptest.Server
	store          fakeSessionStore
	nodePrivate    ed25519.PrivateKey
	controlPrivate ed25519.PrivateKey
	credential     string
	origin         string
}

func newHubFixture(t *testing.T) hubFixture {
	t.Helper()
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(nil)
	controlPublic, controlPrivate, _ := ed25519.GenerateKey(nil)
	credential := "synthetic-node-connection-credential"
	credentialHash := sha256.Sum256([]byte(credential))
	store := fakeSessionStore{
		node:    serverstore.NodeSession{OwnerID: "own_test", NodeID: "nod_test", PublicKey: nodePublic, CredentialHash: credentialHash[:], Status: "active"},
		control: serverstore.ControlClientSession{OwnerID: "own_test", ClientID: "cli_test", KeyID: "key_test", PublicKey: controlPublic, Status: "active"},
	}
	origin := "https://control.example.test"
	hub, err := NewHub(store, HubOptions{AllowedControlOrigins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /node/connect", hub.NodeHandler)
	mux.HandleFunc("GET /web/connect", hub.ControlHandler)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(func() { _ = hub.Close(); server.Close() })
	return hubFixture{hub: hub, server: server, store: store, nodePrivate: nodePrivate, controlPrivate: controlPrivate, credential: credential, origin: origin}
}

func (f hubFixture) dialNode(t *testing.T) transport.Transport {
	t.Helper()
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", f.store.node.NodeID)
	header.Set("Authorization", "Bearer "+f.credential)
	result, _, err := transport.DialRelay(context.Background(), wssURL(f.server.URL)+"/node/connect", transport.RelayDialOptions{
		HTTPClient: f.server.Client(), Header: header, Role: transport.SessionRoleNode, SubjectID: f.store.node.NodeID,
		Sign:  func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(f.nodePrivate, input), nil },
		Relay: transport.RelayOptions{MaxSendBytes: 1 << 20, MaxReceiveBytes: 256 << 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f hubFixture) dialControl(t *testing.T) transport.Transport {
	t.Helper()
	header := make(http.Header)
	header.Set("X-Yuanshu-Client-ID", f.store.control.ClientID)
	header.Set("Origin", f.origin)
	result, _, err := transport.DialRelay(context.Background(), wssURL(f.server.URL)+"/web/connect", transport.RelayDialOptions{
		HTTPClient: f.server.Client(), Header: header, Role: transport.SessionRoleControl, SubjectID: f.store.control.ClientID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(f.controlPrivate, input), nil
		},
		Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHubControlBrowserQueryAuthenticationAndConflict(t *testing.T) {
	fixture := newHubFixture(t)
	header := make(http.Header)
	header.Set("Origin", fixture.origin)
	control, _, err := transport.DialRelay(context.Background(), wssURL(fixture.server.URL)+"/web/connect?clientId="+fixture.store.control.ClientID, transport.RelayDialOptions{
		HTTPClient: fixture.server.Client(), Header: header, Role: transport.SessionRoleControl, SubjectID: fixture.store.control.ClientID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(fixture.controlPrivate, input), nil
		},
		Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = control.Close()

	header.Set("X-Yuanshu-Client-ID", "cli_conflict")
	_, response, err := transport.DialRelay(context.Background(), wssURL(fixture.server.URL)+"/web/connect?clientId="+fixture.store.control.ClientID, transport.RelayDialOptions{
		HTTPClient: fixture.server.Client(), Header: header, Role: transport.SessionRoleControl, SubjectID: fixture.store.control.ClientID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(fixture.controlPrivate, input), nil
		},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("conflict response=%v error=%v", response, err)
	}
}

func TestHubRoutesSignedControlsAndRawEvents(t *testing.T) {
	fixture := newHubFixture(t)
	node := fixture.dialNode(t)
	defer node.Close()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)

	controlRaw := signedControl(t, fixture, protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(controlRaw)); err != nil {
		t.Fatal(err)
	}
	fromControl, err := node.Receive(context.Background())
	if err != nil || !bytes.Equal(fromControl.Bytes(), controlRaw) {
		t.Fatalf("node received=%q err=%v", fromControl.Bytes(), err)
	}
	eventRaw, err := json.Marshal(protocolv1.YuanshuMessage{
		ProtocolVersion: protocolv1.CurrentVersion, MessageID: "event-runtime-ready", Type: string(protocolv1.EventRuntimeStatus),
		SentAt: time.Now().UTC().Format(time.RFC3339Nano), OwnerID: "own_test", NodeID: "nod_test",
		StreamID: "node-events", Sequence: 1, CorrelationID: "event-runtime-ready", Payload: map[string]any{"status": "ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Send(context.Background(), transport.NewFrame(eventRaw)); err != nil {
		t.Fatal(err)
	}
	fromNode, err := control.Receive(context.Background())
	if err != nil || !bytes.Equal(fromNode.Bytes(), eventRaw) {
		t.Fatalf("control received=%q err=%v", fromNode.Bytes(), err)
	}
	detail, ok := fixture.hub.OwnerNodeDetail("own_test", "nod_test")
	if !ok || !detail.Online || detail.RuntimeStatus != "ready" || detail.LastEventAt.IsZero() {
		t.Fatalf("node detail=%+v found=%v", detail, ok)
	}
}

func TestHubRejectsReplayedForwardedControlWithoutClosingSession(t *testing.T) {
	fixture := newHubFixture(t)
	node := fixture.dialNode(t)
	defer node.Close()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)

	request := signedControl(t, fixture, protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := node.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := control.Send(context.Background(), transport.NewFrame(request)); err != nil {
		t.Fatal(err)
	}
	rejected, err := receiveJSONType(control, string(protocolv1.EventControlResult))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := rejected["payload"].(map[string]any)
	if rejected["correlationId"] != "control-1" || payload["status"] != string(protocolv1.ControlResultRejected) || payload["errorCode"] != string(protocolv1.ErrorReplay) {
		t.Fatalf("replay result=%v", rejected)
	}

	next := signedControl(t, fixture, protocolv1.ControlDeviceSync, 2, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(next)); err != nil {
		t.Fatal(err)
	}
	forwarded, err := node.Receive(context.Background())
	if err != nil || !bytes.Equal(forwarded.Bytes(), next) {
		t.Fatalf("session did not survive replay: %q %v", forwarded.Bytes(), err)
	}
}

func TestHubKeepsControlSessionAliveWhenTargetNodeIsOffline(t *testing.T) {
	fixture := newHubFixture(t)
	node := fixture.dialNode(t)
	defer node.Close()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)

	offline := signedControlForNode(t, fixture, "offline-node", protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(offline)); err != nil {
		t.Fatal(err)
	}
	valid := signedControl(t, fixture, protocolv1.ControlDeviceSync, 2, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(valid)); err != nil {
		t.Fatal(err)
	}
	got, err := node.Receive(context.Background())
	if err != nil || !bytes.Equal(got.Bytes(), valid) {
		t.Fatalf("control session did not survive offline target: frame=%q err=%v", got.Bytes(), err)
	}
}

func TestHubLeaseGuardsTurnControlsAndAllowsCurrentHolder(t *testing.T) {
	fixture := newHubFixture(t)
	node := fixture.dialNode(t)
	defer node.Close()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)

	withoutLease := signedControl(t, fixture, protocolv1.ControlTurnStart, 1, map[string]any{"input": "blocked"}, "workspace", "thread", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(withoutLease)); err != nil {
		t.Fatal(err)
	}
	blocked, err := receiveJSON(control)
	if err != nil || blocked["type"] != string(protocolv1.EventControlResult) || blocked["correlationId"] != "control-1" {
		t.Fatalf("blocked result=%v err=%v", blocked, err)
	}
	shortContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := node.Receive(shortContext); err == nil {
		t.Fatal("turn without lease reached Node")
	}

	acquire := signedControl(t, fixture, protocolv1.ControlLeaseAcquire, 2, map[string]any{}, "workspace", "thread", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(acquire)); err != nil {
		t.Fatal(err)
	}
	acquired, err := receiveJSONType(control, string(protocolv1.EventControlResult))
	if err != nil {
		t.Fatal(err)
	}
	leasePayload, ok := acquired["payload"].(map[string]any)
	if !ok {
		t.Fatalf("acquire payload=%v", acquired)
	}
	lease, ok := leasePayload["lease"].(map[string]any)
	if !ok {
		t.Fatalf("acquire lease=%v", leasePayload)
	}
	leaseID, _ := lease["leaseId"].(string)
	epoch, _ := lease["epoch"].(float64)
	withLease := signedControl(t, fixture, protocolv1.ControlTurnStart, 3, map[string]any{"input": "allowed", "lease": map[string]any{"leaseId": leaseID, "epoch": epoch}}, "workspace", "thread", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(withLease)); err != nil {
		t.Fatal(err)
	}
	forwarded, err := node.Receive(context.Background())
	if err != nil || string(forwarded.Bytes()) != string(withLease) {
		t.Fatalf("leased turn=%q err=%v", forwarded.Bytes(), err)
	}
}

func TestHubNotificationsStayOwnerScopedAndCanBeMarkedRead(t *testing.T) {
	fixture := newHubFixture(t)
	node := fixture.dialNode(t)
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)
	fixture.hub.mu.Lock()
	fixture.hub.nodes["other-owner\x00other-node"] = &hubConnection{ownerID: "other-owner", subjectID: "other-node", role: transport.SessionRoleNode}
	fixture.hub.mu.Unlock()
	if err := fixture.hub.notifications.SaveNotification(context.Background(), serverstore.Notification{ID: "notice", OwnerID: "own_test", NodeID: "nod_test", Type: "task.completed", Summary: "任务已完成", SourceSequence: 7, DedupKey: "turn:7", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	list := signedControl(t, fixture, protocolv1.ControlNotificationsList, 1, map[string]any{"limit": 20}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(list)); err != nil {
		t.Fatal(err)
	}
	result, err := receiveJSONType(control, string(protocolv1.EventControlResult))
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok || result["correlationId"] != "control-1" {
		t.Fatalf("list result=%v", result)
	}
	items, ok := payload["notifications"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("notification list=%v", payload)
	}
	online, ok := payload["onlineNodeIds"].([]any)
	if !ok || len(online) != 1 || online[0] != "nod_test" {
		t.Fatalf("online nodes=%v", payload["onlineNodeIds"])
	}
	fixture.hub.mu.Lock()
	delete(fixture.hub.nodes, "other-owner\x00other-node")
	fixture.hub.mu.Unlock()
	if err := node.Close(); err != nil {
		t.Fatal(err)
	}
	waitHubSnapshot(t, fixture.hub, 0, 1)
	offlineList := signedControl(t, fixture, protocolv1.ControlNotificationsList, 2, map[string]any{"limit": 20}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(offlineList)); err != nil {
		t.Fatal(err)
	}
	offlineResult, err := receiveJSONType(control, string(protocolv1.EventControlResult))
	if err != nil {
		t.Fatal(err)
	}
	offlinePayload, ok := offlineResult["payload"].(map[string]any)
	if !ok {
		t.Fatalf("offline list result=%v", offlineResult)
	}
	offlineNodes, ok := offlinePayload["onlineNodeIds"].([]any)
	if !ok || len(offlineNodes) != 0 {
		t.Fatalf("offline nodes=%v", offlinePayload["onlineNodeIds"])
	}
	read := signedControl(t, fixture, protocolv1.ControlNotificationsRead, 3, map[string]any{"notificationId": "notice"}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(read)); err != nil {
		t.Fatal(err)
	}
	if _, err := receiveJSONType(control, string(protocolv1.EventControlResult)); err != nil {
		t.Fatal(err)
	}
}

func signedControl(t *testing.T, fixture hubFixture, kind protocolv1.ControlType, sequence int64, payload map[string]any, workspaceID, threadID, turnID, itemID string) []byte {
	return signedControlForNode(t, fixture, fixture.store.node.NodeID, kind, sequence, payload, workspaceID, threadID, turnID, itemID)
}

func signedControlForNode(t *testing.T, fixture hubFixture, nodeID string, kind protocolv1.ControlType, sequence int64, payload map[string]any, workspaceID, threadID, turnID, itemID string) []byte {
	t.Helper()
	return signedRoutedControl(t, fixture.store.control.OwnerID, nodeID, fixture.store.control.ClientID, fixture.store.control.KeyID, fixture.controlPrivate, kind, sequence, payload, workspaceID, threadID, turnID, itemID)
}

func signedRoutedControl(t *testing.T, ownerID, nodeID, clientID, keyID string, private ed25519.PrivateKey, kind protocolv1.ControlType, sequence int64, payload map[string]any, workspaceID, threadID, turnID, itemID string) []byte {
	t.Helper()
	now := time.Now().UTC()
	message := protocolv1.YuanshuMessage{ProtocolVersion: protocolv1.CurrentVersion, MessageID: "control-" + fmt.Sprint(sequence), Type: string(kind), SentAt: now.Format(time.RFC3339Nano), ExpiresAt: stringPtr(now.Add(time.Minute).Format(time.RFC3339Nano)), OwnerID: ownerID, NodeID: nodeID, StreamID: "control-stream", Sequence: sequence, CorrelationID: "control-" + fmt.Sprint(sequence), Nonce: stringPtr(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(sequence)}, 16))), Signer: &protocolv1.Signer{ClientID: clientID, KeyID: keyID}, Payload: payload}
	if workspaceID != "" {
		message.WorkspaceID = stringPtr(workspaceID)
	}
	if threadID != "" {
		message.ThreadID = stringPtr(threadID)
	}
	if turnID != "" {
		message.TurnID = stringPtr(turnID)
	}
	if itemID != "" {
		message.ItemID = stringPtr(itemID)
	}
	input, err := protocolv1.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, input)
	encoded := base64.RawURLEncoding.EncodeToString(signature)
	message.Signature = &encoded
	result, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func receiveJSON(connection transport.Transport) (map[string]any, error) {
	frame, err := connection.Receive(context.Background())
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(frame.Bytes(), &result)
	return result, err
}

func receiveJSONType(connection transport.Transport, messageType string) (map[string]any, error) {
	for attempt := 0; attempt < 4; attempt++ {
		message, err := receiveJSON(connection)
		if err != nil {
			return nil, err
		}
		if message["type"] == messageType {
			return message, nil
		}
	}
	return nil, fmt.Errorf("message type %q not received", messageType)
}

func TestHubRoutesRemoteControlThroughStandaloneLocalNode(t *testing.T) {
	fixture := newHubFixture(t)
	serverSide, nodeSide, err := transport.NewStandalonePair(transport.StandaloneOptions{QueueCapacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeSide.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attached := make(chan error, 1)
	go func() { attached <- fixture.hub.AttachLocalNode(ctx, "own_test", "nod_test", serverSide) }()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 1, 1)

	controlRaw := signedControl(t, fixture, protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(controlRaw)); err != nil {
		t.Fatal(err)
	}
	received, err := nodeSide.Receive(context.Background())
	if err != nil || !bytes.Equal(received.Bytes(), controlRaw) {
		t.Fatalf("local Node received=%q err=%v", received.Bytes(), err)
	}
	eventRaw := []byte(`{"protocolVersion":"1.0","type":"runtime.status","ownerId":"own_test","nodeId":"nod_test","payload":{"status":"ready"}}`)
	if err := nodeSide.Send(context.Background(), transport.NewFrame(eventRaw)); err != nil {
		t.Fatal(err)
	}
	forwarded, err := control.Receive(context.Background())
	if err != nil || !bytes.Equal(forwarded.Bytes(), eventRaw) {
		t.Fatalf("Control received=%q err=%v", forwarded.Bytes(), err)
	}
	cancel()
	if err := <-attached; err != nil {
		t.Fatalf("AttachLocalNode() = %v", err)
	}
}

func TestHubKeepsStandaloneAndRemoteNodeStreamsIndependent(t *testing.T) {
	fixture := newHubFixture(t)
	remote := fixture.dialNode(t)
	defer remote.Close()
	serverSide, localSide, err := transport.NewStandalonePair(transport.StandaloneOptions{QueueCapacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer localSide.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attached := make(chan error, 1)
	go func() { attached <- fixture.hub.AttachLocalNode(ctx, "own_test", "nod_local", serverSide) }()
	control := fixture.dialControl(t)
	defer control.Close()
	waitHubSnapshot(t, fixture.hub, 2, 1)
	localRaw := signedControlForNode(t, fixture, "nod_local", protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	remoteRaw := signedControl(t, fixture, protocolv1.ControlDeviceSync, 2, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(localRaw)); err != nil {
		t.Fatal(err)
	}
	if got, err := localSide.Receive(context.Background()); err != nil || !bytes.Equal(got.Bytes(), localRaw) {
		t.Fatalf("local route err=%v", err)
	}
	short, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if _, err := remote.Receive(short); err != context.DeadlineExceeded {
		t.Fatalf("local frame crossed to remote: %v", err)
	}
	shortCancel()
	if err := control.Send(context.Background(), transport.NewFrame(remoteRaw)); err != nil {
		t.Fatal(err)
	}
	if got, err := remote.Receive(context.Background()); err != nil || !bytes.Equal(got.Bytes(), remoteRaw) {
		t.Fatalf("remote route err=%v", err)
	}
	cancel()
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
}

func TestHubRejectsPlaintextOriginCredentialAndTargetSpoofing(t *testing.T) {
	fixture := newHubFixture(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/node/connect", nil)
	response := httptest.NewRecorder()
	fixture.hub.NodeHandler(response, request)
	if response.Code != http.StatusUpgradeRequired || !strings.Contains(response.Body.String(), "tls_required") {
		t.Fatalf("plaintext status=%d body=%q", response.Code, response.Body.String())
	}

	badHeader := make(http.Header)
	badHeader.Set("X-Yuanshu-Node-ID", fixture.store.node.NodeID)
	badHeader.Set("Authorization", "Bearer wrong-credential-canary")
	_, handshake, err := transport.DialRelay(context.Background(), wssURL(fixture.server.URL)+"/node/connect", transport.RelayDialOptions{
		HTTPClient: fixture.server.Client(), Header: badHeader, Role: transport.SessionRoleNode, SubjectID: fixture.store.node.NodeID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(fixture.nodePrivate, input), nil
		},
		Relay: transport.RelayOptions{MaxSendBytes: 1 << 20, MaxReceiveBytes: 256 << 10},
	})
	if err == nil || handshake == nil || handshake.StatusCode != http.StatusUnauthorized || strings.Contains(err.Error(), "wrong-credential-canary") {
		t.Fatalf("credential response=%v err=%v", handshake, err)
	}
	wrongOrigin := make(http.Header)
	wrongOrigin.Set("X-Yuanshu-Client-ID", fixture.store.control.ClientID)
	wrongOrigin.Set("Origin", "https://wrong.example.test")
	_, handshake, err = transport.DialRelay(context.Background(), wssURL(fixture.server.URL)+"/web/connect", transport.RelayDialOptions{
		HTTPClient: fixture.server.Client(), Header: wrongOrigin, Role: transport.SessionRoleControl, SubjectID: fixture.store.control.ClientID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(fixture.controlPrivate, input), nil
		},
		Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20},
	})
	if err == nil || handshake == nil || handshake.StatusCode != http.StatusForbidden {
		t.Fatalf("origin response=%v err=%v", handshake, err)
	}
	validControlHeader := make(http.Header)
	validControlHeader.Set("X-Yuanshu-Client-ID", fixture.store.control.ClientID)
	validControlHeader.Set("Origin", fixture.origin)
	_, _, err = transport.DialRelay(context.Background(), wssURL(fixture.server.URL)+"/web/connect", transport.RelayDialOptions{
		HTTPClient: fixture.server.Client(), Header: validControlHeader, Role: transport.SessionRoleControl, SubjectID: fixture.store.control.ClientID,
		Sign:  func(context.Context, []byte) ([]byte, error) { return bytes.Repeat([]byte{0x7f}, 64), nil },
		Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20},
	})
	if err == nil || strings.Contains(err.Error(), base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x7f}, 64))) {
		t.Fatalf("signature error=%v", err)
	}

	node := fixture.dialNode(t)
	defer node.Close()
	control := fixture.dialControl(t)
	spoof := transport.NewFrame([]byte(`{"protocolVersion":"1.0","type":"device.sync","ownerId":"other","nodeId":"nod_test"}`))
	if err := control.Send(context.Background(), spoof); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := control.Receive(ctx); err == nil {
		t.Fatal("spoofing did not close the control connection")
	}
	short, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	if _, err := node.Receive(short); err != context.DeadlineExceeded {
		t.Fatalf("spoofed frame reached node or wrong error=%v", err)
	}
}

func TestHubRejectsExpiredChallengeAndRepeatedAuthentication(t *testing.T) {
	fixture := newHubFixture(t)
	now := time.Now().UTC()
	var clockCalls int
	clock := func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return now
		}
		return now.Add(time.Minute)
	}
	expiredHub, err := NewHub(fixture.store, HubOptions{AllowedControlOrigins: []string{fixture.origin}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer expiredHub.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /node/connect", expiredHub.NodeHandler)
	tlsServer := httptest.NewTLSServer(mux)
	defer tlsServer.Close()
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", fixture.store.node.NodeID)
	header.Set("Authorization", "Bearer "+fixture.credential)
	_, _, err = transport.DialRelay(context.Background(), wssURL(tlsServer.URL)+"/node/connect", transport.RelayDialOptions{
		HTTPClient: tlsServer.Client(), Header: header, Role: transport.SessionRoleNode, SubjectID: fixture.store.node.NodeID,
		Sign: func(_ context.Context, input []byte) ([]byte, error) {
			return ed25519.Sign(fixture.nodePrivate, input), nil
		},
		Relay: transport.RelayOptions{MaxSendBytes: 1 << 20, MaxReceiveBytes: 256 << 10}, Clock: func() time.Time { return now },
	})
	if err == nil {
		t.Fatal("expired challenge authenticated")
	}

	node := fixture.dialNode(t)
	defer node.Close()
	repeated := transport.NewFrame([]byte(`{"version":"1","type":"authenticate","signature":"synthetic"}`))
	if err := node.Send(context.Background(), repeated); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := node.Receive(ctx); err == nil {
		t.Fatal("repeated authentication remained connected")
	}
}

func TestHubReplacesDuplicateNodeAndClosesIdempotently(t *testing.T) {
	fixture := newHubFixture(t)
	first := fixture.dialNode(t)
	defer first.Close()
	second := fixture.dialNode(t)
	defer second.Close()
	waitHubSnapshot(t, fixture.hub, 1, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := first.Receive(ctx); err == nil {
		t.Fatal("replaced node connection remained open")
	}
	if err := fixture.hub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.hub.Close(); err != nil || fixture.hub.Snapshot().Status != "closed" {
		t.Fatalf("second close=%v snapshot=%+v", err, fixture.hub.Snapshot())
	}
}

func waitHubSnapshot(t *testing.T, hub *Hub, nodes, controls int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := hub.Snapshot()
		if snapshot.NodeConnections == nodes && snapshot.ControlConnections == controls {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("hub snapshot=%+v", hub.Snapshot())
}

func wssURL(value string) string { return "wss" + strings.TrimPrefix(value, "https") }
