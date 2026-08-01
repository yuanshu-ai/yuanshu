package v1

// Yuanshu Protocol v1 control and event envelope.
type YuanshuMessage struct {
	CorrelationID   string                 `json:"correlationId"`
	ExpiresAt       *string                `json:"expiresAt,omitempty"`
	ItemID          *string                `json:"itemId,omitempty"`
	MessageID       string                 `json:"messageId"`
	NodeID          string                 `json:"nodeId"`
	Nonce           *string                `json:"nonce,omitempty"`
	OwnerID         string                 `json:"ownerId"`
	Payload         map[string]interface{} `json:"payload"`
	ProtocolVersion string                 `json:"protocolVersion"`
	SentAt          string                 `json:"sentAt"`
	Sequence        int64                  `json:"sequence"`
	Signature       *string                `json:"signature,omitempty"`
	Signer          *Signer                `json:"signer,omitempty"`
	StreamID        string                 `json:"streamId"`
	ThreadID        *string                `json:"threadId,omitempty"`
	TurnID          *string                `json:"turnId,omitempty"`
	Type            string                 `json:"type"`
	WorkspaceID     *string                `json:"workspaceId,omitempty"`
}

type Signer struct {
	ClientID string `json:"clientId"`
	KeyID    string `json:"keyId"`
}
