// Package node assembles the local Yuanshu Node process.
package node

import "sync"

const LocalStatusVersion = 1

type Status struct {
	Version            int    `json:"version"`
	State              string `json:"state"`
	Platform           string `json:"platform"`
	Config             string `json:"config"`
	Identity           string `json:"identity"`
	IdentityStorage    string `json:"identityStorage,omitempty"`
	Database           string `json:"database"`
	Workspaces         int    `json:"workspaces"`
	Codex              string `json:"codex"`
	Authentication     string `json:"authentication"`
	NodeAuthentication string `json:"nodeAuthentication,omitempty"`
	Recovery           string `json:"recovery"`
	RemoteControl      string `json:"remoteControl"`
	RelayLastError     string `json:"relayLastError,omitempty"`
	RelayLastSeen      string `json:"relayLastSeen,omitempty"`
	Compatibility      string `json:"compatibility,omitempty"`
	WorkspaceStatus    string `json:"workspaceStatus,omitempty"`
	Credential         string `json:"credential,omitempty"`
	Autostart          string `json:"autostart"`
}

type statusStore struct {
	mu     sync.RWMutex
	status Status
}

func newStatusStore(family string) *statusStore {
	return &statusStore{status: Status{
		Version: LocalStatusVersion, State: "starting", Platform: family,
		Config: "unchecked", Identity: "unchecked", IdentityStorage: "local_file", Database: "unchecked",
		Codex: "unchecked", Authentication: "unchecked", NodeAuthentication: "device_signature", Recovery: "not_required",
		RemoteControl: "not_available", WorkspaceStatus: "unchecked", Credential: "unchecked", Autostart: "unchecked",
	}}
}

func (s *statusStore) snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *statusStore) update(change func(*Status)) Status {
	s.mu.Lock()
	change(&s.status)
	result := s.status
	s.mu.Unlock()
	return result
}
