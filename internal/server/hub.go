package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

const (
	defaultAuthenticationTimeout = 5 * time.Second
	defaultChallengeTTL          = 30 * time.Second
	defaultConnectionLimit       = 64
)

type sessionStore interface {
	NodeSession(context.Context, string) (serverstore.NodeSession, error)
	ControlClientSession(context.Context, string) (serverstore.ControlClientSession, error)
}

type HubOptions struct {
	Random                io.Reader
	Clock                 func() time.Time
	AllowedControlOrigins []string
	QueueCapacity         int
	AuthenticationTimeout time.Duration
	ChallengeTTL          time.Duration
	HeartbeatInterval     time.Duration
	IdleTimeout           time.Duration
	MaxConnections        int
}

type HubSnapshot struct {
	Status             string `json:"status"`
	NodeConnections    int    `json:"nodeConnections"`
	ControlConnections int    `json:"controlConnections"`
}

type Hub struct {
	store        sessionStore
	random       io.Reader
	clock        func() time.Time
	origins      map[string]struct{}
	authTimeout  time.Duration
	challengeTTL time.Duration
	relayOptions transport.RelayOptions
	limit        chan struct{}
	mu           sync.RWMutex
	nodes        map[string]*hubConnection
	controls     map[string]*hubConnection
	closed       bool
}

type hubConnection struct {
	ownerID   string
	subjectID string
	role      transport.SessionRole
	relay     transport.Transport
}

func NewHub(store sessionStore, options HubOptions) (*Hub, error) {
	if store == nil || options.QueueCapacity < 0 || options.AuthenticationTimeout < 0 || options.ChallengeTTL < 0 || options.HeartbeatInterval < 0 || options.IdleTimeout < 0 || options.MaxConnections < 0 {
		return nil, ErrInvalid
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	authTimeout := options.AuthenticationTimeout
	if authTimeout == 0 {
		authTimeout = defaultAuthenticationTimeout
	}
	challengeTTL := options.ChallengeTTL
	if challengeTTL == 0 {
		challengeTTL = defaultChallengeTTL
	}
	connectionLimit := options.MaxConnections
	if connectionLimit == 0 {
		connectionLimit = defaultConnectionLimit
	}
	origins := make(map[string]struct{}, len(options.AllowedControlOrigins))
	for _, origin := range options.AllowedControlOrigins {
		if origin == "" || origin != strings.TrimSpace(origin) {
			return nil, ErrInvalid
		}
		origins[origin] = struct{}{}
	}
	return &Hub{
		store: store, random: random, clock: clock, origins: origins, authTimeout: authTimeout, challengeTTL: challengeTTL,
		relayOptions: transport.RelayOptions{QueueCapacity: options.QueueCapacity, HeartbeatInterval: options.HeartbeatInterval, IdleTimeout: options.IdleTimeout},
		limit:        make(chan struct{}, connectionLimit), nodes: make(map[string]*hubConnection), controls: make(map[string]*hubConnection),
	}, nil
}

func (h *Hub) NodeHandler(writer http.ResponseWriter, request *http.Request) {
	if request.TLS == nil {
		writeError(writer, http.StatusUpgradeRequired, "tls_required")
		return
	}
	if request.Header.Get("Origin") != "" {
		writeError(writer, http.StatusForbidden, "forbidden")
		return
	}
	if !h.acquire() {
		writeError(writer, http.StatusServiceUnavailable, "connection_limit")
		return
	}
	defer h.release()
	nodeID := request.Header.Get("X-Yuanshu-Node-ID")
	credential, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok || !validOpaque(nodeID, 128) {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := h.store.NodeSession(request.Context(), nodeID)
	if err != nil || record.Status != "active" || len(record.PublicKey) != ed25519.PublicKeySize || len(record.CredentialHash) != sha256.Size {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	digest := sha256.Sum256([]byte(credential))
	if subtle.ConstantTimeCompare(digest[:], record.CredentialHash) != 1 {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	h.serve(writer, request, transport.SessionRoleNode, record.OwnerID, record.NodeID, record.PublicKey)
}

func (h *Hub) ControlHandler(writer http.ResponseWriter, request *http.Request) {
	if request.TLS == nil {
		writeError(writer, http.StatusUpgradeRequired, "tls_required")
		return
	}
	if _, ok := h.origins[request.Header.Get("Origin")]; !ok {
		writeError(writer, http.StatusForbidden, "forbidden")
		return
	}
	if !h.acquire() {
		writeError(writer, http.StatusServiceUnavailable, "connection_limit")
		return
	}
	defer h.release()
	clientID := request.Header.Get("X-Yuanshu-Client-ID")
	if !validOpaque(clientID, 128) {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := h.store.ControlClientSession(request.Context(), clientID)
	if err != nil || record.Status != "active" || len(record.PublicKey) != ed25519.PublicKeySize {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	h.serve(writer, request, transport.SessionRoleControl, record.OwnerID, record.ClientID, record.PublicKey)
}

func (h *Hub) serve(writer http.ResponseWriter, request *http.Request, role transport.SessionRole, ownerID, subjectID string, publicKey []byte) {
	conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		Subprotocols: []string{transport.RelaySubprotocol}, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	if conn.Subprotocol() != transport.RelaySubprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol required")
		return
	}
	authCtx, cancel := context.WithTimeout(request.Context(), h.authTimeout)
	challenge, err := h.authenticate(authCtx, conn, role, subjectID, publicKey)
	cancel()
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	_ = challenge
	options := h.relayOptions
	if role == transport.SessionRoleNode {
		options.MaxSendBytes = protocolv1.EventFrameMaxBytes
		options.MaxReceiveBytes = protocolv1.ControlFrameMaxBytes
	} else {
		options.MaxSendBytes = protocolv1.ControlFrameMaxBytes
		options.MaxReceiveBytes = protocolv1.EventFrameMaxBytes
	}
	relay, err := transport.NewRelay(conn, options)
	if err != nil {
		_ = conn.CloseNow()
		return
	}
	connection := &hubConnection{ownerID: ownerID, subjectID: subjectID, role: role, relay: relay}
	if !h.register(connection) {
		_ = relay.Close()
		return
	}
	defer func() {
		h.unregister(connection)
		_ = relay.Close()
	}()
	h.route(request.Context(), connection)
}

func (h *Hub) authenticate(ctx context.Context, conn *websocket.Conn, role transport.SessionRole, subjectID string, publicKey []byte) (transport.SessionChallenge, error) {
	connectionID, err := h.randomValue(16)
	if err != nil {
		return transport.SessionChallenge{}, err
	}
	nonce, err := h.randomValue(32)
	if err != nil {
		return transport.SessionChallenge{}, err
	}
	challenge := transport.SessionChallenge{
		Version: "1", Type: "challenge", Role: role, ConnectionID: connectionID, SubjectID: subjectID,
		Nonce: nonce, ExpiresAt: h.clock().UTC().Add(h.challengeTTL).Format(time.RFC3339Nano),
	}
	encoded, _ := json.Marshal(challenge)
	if err := conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		return transport.SessionChallenge{}, errors.New("session authentication failed")
	}
	var response transport.SessionAuthentication
	if err := readStrictWebSocketJSON(ctx, conn, &response); err != nil || response.Version != "1" || response.Type != "authenticate" || !h.clock().UTC().Before(mustParseTime(challenge.ExpiresAt)) {
		return transport.SessionChallenge{}, errors.New("session authentication failed")
	}
	signature, err := base64.RawURLEncoding.DecodeString(response.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != response.Signature {
		return transport.SessionChallenge{}, errors.New("session authentication failed")
	}
	input, err := transport.SessionSigningInput(challenge)
	if err != nil || !ed25519.Verify(publicKey, input, signature) {
		return transport.SessionChallenge{}, errors.New("session authentication failed")
	}
	ready, _ := json.Marshal(transport.SessionReady{Version: "1", Type: "authenticated"})
	if err := conn.Write(ctx, websocket.MessageText, ready); err != nil {
		return transport.SessionChallenge{}, errors.New("session authentication failed")
	}
	return challenge, nil
}

func (h *Hub) route(ctx context.Context, source *hubConnection) {
	for {
		frame, err := source.relay.Receive(ctx)
		if err != nil {
			return
		}
		header, err := classifyRoutedFrame(frame, source.role)
		if err != nil || header.OwnerID != source.ownerID {
			return
		}
		if source.role == transport.SessionRoleNode {
			if header.NodeID != source.subjectID {
				return
			}
			h.broadcast(source.ownerID, frame)
			continue
		}
		target := h.node(source.ownerID, header.NodeID)
		if target == nil {
			return
		}
		if err := target.relay.Send(ctx, frame); err != nil {
			_ = target.relay.Close()
			return
		}
	}
}

type routeHeader struct {
	ProtocolVersion string `json:"protocolVersion"`
	Type            string `json:"type"`
	OwnerID         string `json:"ownerId"`
	NodeID          string `json:"nodeId"`
}

func classifyRoutedFrame(frame transport.Frame, role transport.SessionRole) (routeHeader, error) {
	var header routeHeader
	if err := json.Unmarshal(frame.Bytes(), &header); err != nil || header.OwnerID == "" || header.NodeID == "" || header.Type == "" {
		return routeHeader{}, errors.New("routed frame is invalid")
	}
	if role == transport.SessionRoleControl {
		if protocolv1.Classify(header.ProtocolVersion, protocolv1.MessageKindControl, header.Type) != protocolv1.ClassificationKnownControl {
			return routeHeader{}, errors.New("routed frame is invalid")
		}
		return header, nil
	}
	classification := protocolv1.Classify(header.ProtocolVersion, protocolv1.MessageKindEvent, header.Type)
	if classification != protocolv1.ClassificationKnownEvent && classification != protocolv1.ClassificationUnknownEvent {
		return routeHeader{}, errors.New("routed frame is invalid")
	}
	return header, nil
}

func (h *Hub) register(connection *hubConnection) bool {
	key := connection.ownerID + "\x00" + connection.subjectID
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false
	}
	var previous *hubConnection
	if connection.role == transport.SessionRoleNode {
		previous = h.nodes[key]
		h.nodes[key] = connection
	} else {
		previous = h.controls[key]
		h.controls[key] = connection
	}
	h.mu.Unlock()
	if previous != nil {
		_ = previous.relay.Close()
	}
	return true
}

func (h *Hub) unregister(connection *hubConnection) {
	key := connection.ownerID + "\x00" + connection.subjectID
	h.mu.Lock()
	if connection.role == transport.SessionRoleNode {
		if h.nodes[key] == connection {
			delete(h.nodes, key)
		}
	} else if h.controls[key] == connection {
		delete(h.controls, key)
	}
	h.mu.Unlock()
}

func (h *Hub) node(ownerID, nodeID string) *hubConnection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nodes[ownerID+"\x00"+nodeID]
}

func (h *Hub) broadcast(ownerID string, frame transport.Frame) {
	h.mu.RLock()
	var targets []*hubConnection
	for _, connection := range h.controls {
		if connection.ownerID == ownerID {
			targets = append(targets, connection)
		}
	}
	h.mu.RUnlock()
	for _, target := range targets {
		if err := target.relay.Send(context.Background(), frame); err != nil {
			_ = target.relay.Close()
		}
	}
}

func (h *Hub) Snapshot() HubSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	status := "ready"
	if h.closed {
		status = "closed"
	}
	return HubSnapshot{Status: status, NodeConnections: len(h.nodes), ControlConnections: len(h.controls)}
}

func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	var connections []*hubConnection
	for _, connection := range h.nodes {
		connections = append(connections, connection)
	}
	for _, connection := range h.controls {
		connections = append(connections, connection)
	}
	h.nodes = make(map[string]*hubConnection)
	h.controls = make(map[string]*hubConnection)
	h.mu.Unlock()
	for _, connection := range connections {
		_ = connection.relay.Close()
	}
	return nil
}

func (h *Hub) acquire() bool {
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return false
	}
	select {
	case h.limit <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *Hub) release() { <-h.limit }

func (h *Hub) randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(h.random, value); err != nil {
		return "", errors.New("session random generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func readStrictWebSocketJSON(ctx context.Context, conn *websocket.Conn, target any) error {
	messageType, raw, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		return errors.New("session message is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("session message is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("session message is invalid")
	}
	return nil
}

func mustParseTime(value string) time.Time {
	result, _ := time.Parse(time.RFC3339Nano, value)
	return result
}
