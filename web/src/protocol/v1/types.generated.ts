/**
 * Yuanshu Protocol v1 control and event envelope.
 */
export interface YuanshuMessage {
    readonly correlationId:   string;
    readonly expiresAt?:      string;
    readonly itemId?:         string;
    readonly messageId:       string;
    readonly nodeId:          string;
    readonly nonce?:          string;
    readonly ownerId:         string;
    readonly payload:         { [key: string]: unknown };
    readonly protocolVersion: string;
    readonly sentAt:          string;
    readonly sequence:        number;
    readonly signature?:      string;
    readonly signer?:         Signer;
    readonly streamId:        string;
    readonly threadId?:       string;
    readonly turnId?:         string;
    readonly type:            string;
    readonly workspaceId?:    string;
}

export interface Signer {
    readonly clientId: string;
    readonly keyId:    string;
}
