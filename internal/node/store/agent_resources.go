package store

import (
	"context"
	"database/sql"
)

const (
	AgentRuntimeManaged      = "managed"
	AgentRuntimeDetectedOnly = "detected-only"
	EndpointOwnerNode        = "node"
	EndpointOwnerExternal    = "external"
)

type AgentInstallationRecord struct {
	AdapterType       string
	InstallationState string
	Version           string
	Compatibility     string
	ProcessState      string
	ProcessCount      int
}

type AgentInstanceRecord struct {
	InstanceID     string
	AdapterType    string
	DisplayName    string
	Enabled        bool
	Default        bool
	RuntimeMode    string
	ConfigRevision string
}

type RuntimeEndpointRecord struct {
	EndpointID string
	InstanceID string
	Mode       string
	Ownership  string
}

type WorkspaceAgentRecord struct {
	WorkspaceID string
	InstanceID  string
	Default     bool
}

type TaskBindingRecord struct {
	TaskID                string
	InstanceID            string
	EndpointID            string
	WorkspaceID           string
	NativeSessionID       string
	Ownership             string
	State                 string
	ActiveRunID           string
	LegacyNativeIDExposed bool
}

func (s *Store) SaveAgentInstallation(ctx context.Context, record AgentInstallationRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validAgentInstallation(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO agent_installations(adapter_type,installation_state,version,compatibility,process_state,process_count,detected_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(adapter_type) DO UPDATE SET installation_state=excluded.installation_state,version=excluded.version,
		compatibility=excluded.compatibility,process_state=excluded.process_state,process_count=excluded.process_count,detected_at=excluded.detected_at`,
		record.AdapterType, record.InstallationState, record.Version, record.Compatibility, record.ProcessState, record.ProcessCount, timestamp(s.clock().UTC()))
	if err != nil {
		return internal("agent installation save")
	}
	return nil
}

func (s *Store) ReplaceAgentResources(ctx context.Context, instances []AgentInstanceRecord, endpoints []RuntimeEndpointRecord, links []WorkspaceAgentRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validAgentResources(instances, endpoints, links) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return internal("agent resource transaction")
	}
	defer tx.Rollback()
	now := timestamp(s.clock().UTC())
	if _, err = tx.ExecContext(ctx, "UPDATE agent_instances SET is_default=0"); err != nil {
		return internal("agent instance replace")
	}
	for _, item := range instances {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_instances(instance_id,adapter_type,display_name,enabled,is_default,runtime_mode,config_revision,updated_at)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET adapter_type=excluded.adapter_type,display_name=excluded.display_name,
			enabled=excluded.enabled,is_default=excluded.is_default,runtime_mode=excluded.runtime_mode,config_revision=excluded.config_revision,updated_at=excluded.updated_at`,
			item.InstanceID, item.AdapterType, item.DisplayName, boolInteger(item.Enabled), boolInteger(item.Default), item.RuntimeMode, item.ConfigRevision, now)
		if err != nil {
			return internal("agent instance replace")
		}
	}
	for _, item := range endpoints {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_endpoints(endpoint_id,instance_id,mode,ownership,updated_at)
			VALUES(?,?,?,?,?) ON CONFLICT(endpoint_id) DO UPDATE SET instance_id=excluded.instance_id,mode=excluded.mode,ownership=excluded.ownership,updated_at=excluded.updated_at`,
			item.EndpointID, item.InstanceID, item.Mode, item.Ownership, now)
		if err != nil {
			return internal("runtime endpoint replace")
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM workspace_agents"); err != nil {
		return internal("workspace agent replace")
	}
	for _, item := range links {
		if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_agents(workspace_id,instance_id,is_default,updated_at) VALUES(?,?,?,?)`,
			item.WorkspaceID, item.InstanceID, boolInteger(item.Default), now); err != nil {
			return internal("workspace agent replace")
		}
	}
	if err = tx.Commit(); err != nil {
		return internal("agent resource replace")
	}
	return nil
}

func (s *Store) AgentInstances(ctx context.Context) ([]AgentInstanceRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT instance_id,adapter_type,display_name,enabled,is_default,runtime_mode,config_revision FROM agent_instances ORDER BY instance_id`)
	if err != nil {
		return nil, internal("agent instance list")
	}
	defer rows.Close()
	result := make([]AgentInstanceRecord, 0)
	for rows.Next() {
		var item AgentInstanceRecord
		var enabled, isDefault int
		if err := rows.Scan(&item.InstanceID, &item.AdapterType, &item.DisplayName, &enabled, &isDefault, &item.RuntimeMode, &item.ConfigRevision); err != nil {
			return nil, internal("agent instance list")
		}
		item.Enabled, item.Default = enabled == 1, isDefault == 1
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, internal("agent instance list")
	}
	return result, nil
}

func (s *Store) WorkspaceAgents(ctx context.Context, workspaceID string) ([]WorkspaceAgentRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !validWorkspaceText(workspaceID, 128) {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT workspace_id,instance_id,is_default FROM workspace_agents WHERE workspace_id=? ORDER BY instance_id`, workspaceID)
	if err != nil {
		return nil, internal("workspace agent list")
	}
	defer rows.Close()
	var result []WorkspaceAgentRecord
	for rows.Next() {
		var item WorkspaceAgentRecord
		var d int
		if err := rows.Scan(&item.WorkspaceID, &item.InstanceID, &d); err != nil {
			return nil, internal("workspace agent list")
		}
		item.Default = d == 1
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, internal("workspace agent list")
	}
	return result, nil
}

func (s *Store) SaveTaskBinding(ctx context.Context, record TaskBindingRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validTaskBinding(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	var active any
	if record.ActiveRunID != "" {
		active = record.ActiveRunID
	}
	_, err = db.ExecContext(ctx, `INSERT INTO task_bindings(task_id,instance_id,endpoint_id,workspace_id,native_session_id,ownership,state,active_run_id,legacy_native_id_exposed,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET instance_id=excluded.instance_id,endpoint_id=excluded.endpoint_id,
		workspace_id=excluded.workspace_id,native_session_id=excluded.native_session_id,ownership=excluded.ownership,state=excluded.state,
		active_run_id=excluded.active_run_id,legacy_native_id_exposed=excluded.legacy_native_id_exposed,updated_at=excluded.updated_at`,
		record.TaskID, record.InstanceID, record.EndpointID, record.WorkspaceID, record.NativeSessionID, record.Ownership, record.State, active, boolInteger(record.LegacyNativeIDExposed), timestamp(s.clock().UTC()))
	if err != nil {
		return internal("task binding save")
	}
	return nil
}

func (s *Store) TaskBinding(ctx context.Context, taskID string) (TaskBindingRecord, error) {
	if err := requireContext(ctx); err != nil {
		return TaskBindingRecord{}, err
	}
	if !validWorkspaceText(taskID, 128) {
		return TaskBindingRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return TaskBindingRecord{}, err
	}
	return scanTaskBinding(db.QueryRowContext(ctx, `SELECT task_id,instance_id,endpoint_id,workspace_id,native_session_id,ownership,state,active_run_id,legacy_native_id_exposed FROM task_bindings WHERE task_id=?`, taskID))
}

func (s *Store) TaskBindingByNativeSession(ctx context.Context, instanceID, nativeID string) (TaskBindingRecord, error) {
	if err := requireContext(ctx); err != nil {
		return TaskBindingRecord{}, err
	}
	if !validWorkspaceText(instanceID, 128) || !validWorkspaceText(nativeID, 256) {
		return TaskBindingRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return TaskBindingRecord{}, err
	}
	return scanTaskBinding(db.QueryRowContext(ctx, `SELECT task_id,instance_id,endpoint_id,workspace_id,native_session_id,ownership,state,active_run_id,legacy_native_id_exposed FROM task_bindings WHERE instance_id=? AND native_session_id=?`, instanceID, nativeID))
}

type taskBindingScanner interface{ Scan(...any) error }

func scanTaskBinding(scanner taskBindingScanner) (TaskBindingRecord, error) {
	var item TaskBindingRecord
	var active sql.NullString
	var legacy int
	if err := scanner.Scan(&item.TaskID, &item.InstanceID, &item.EndpointID, &item.WorkspaceID, &item.NativeSessionID, &item.Ownership, &item.State, &active, &legacy); err != nil {
		if err == sql.ErrNoRows {
			return TaskBindingRecord{}, ErrNotFound
		}
		return TaskBindingRecord{}, internal("task binding read")
	}
	if active.Valid {
		item.ActiveRunID = active.String
	}
	item.LegacyNativeIDExposed = legacy == 1
	return item, nil
}

func validAgentInstallation(item AgentInstallationRecord) bool {
	return validWorkspaceText(item.AdapterType, 64) && len(item.Version) <= 128 &&
		(item.InstallationState == "installed" || item.InstallationState == "not_installed" || item.InstallationState == "incompatible" || item.InstallationState == "unavailable") &&
		(item.Compatibility == "known" || item.Compatibility == "unverified" || item.Compatibility == "unsupported") &&
		(item.ProcessState == "running" || item.ProcessState == "stopped" || item.ProcessState == "unknown") && item.ProcessCount >= 0 && item.ProcessCount <= 1024
}

func validAgentResources(instances []AgentInstanceRecord, endpoints []RuntimeEndpointRecord, links []WorkspaceAgentRecord) bool {
	if len(instances) == 0 {
		return false
	}
	ids := map[string]AgentInstanceRecord{}
	defaults := 0
	for _, item := range instances {
		if !validWorkspaceText(item.InstanceID, 128) || !validWorkspaceText(item.AdapterType, 64) || !validWorkspaceText(item.DisplayName, 128) || !validWorkspaceText(item.ConfigRevision, 128) || (item.RuntimeMode != AgentRuntimeManaged && item.RuntimeMode != AgentRuntimeDetectedOnly) {
			return false
		}
		if _, ok := ids[item.InstanceID]; ok {
			return false
		}
		ids[item.InstanceID] = item
		if item.Default {
			defaults++
		}
	}
	if defaults != 1 {
		return false
	}
	endpointIDs := map[string]struct{}{}
	for _, item := range endpoints {
		instance, ok := ids[item.InstanceID]
		if !ok || !validWorkspaceText(item.EndpointID, 128) || item.Mode != instance.RuntimeMode || (item.Ownership != EndpointOwnerNode && item.Ownership != EndpointOwnerExternal) {
			return false
		}
		if item.Mode == AgentRuntimeManaged && item.Ownership != EndpointOwnerNode {
			return false
		}
		if _, ok := endpointIDs[item.EndpointID]; ok {
			return false
		}
		endpointIDs[item.EndpointID] = struct{}{}
	}
	seen := map[string]struct{}{}
	workspaceDefault := map[string]int{}
	workspaces := map[string]struct{}{}
	for _, item := range links {
		instance, ok := ids[item.InstanceID]
		if !ok || !instance.Enabled || instance.RuntimeMode != AgentRuntimeManaged || !validWorkspaceText(item.WorkspaceID, 128) {
			return false
		}
		key := item.WorkspaceID + "\x00" + item.InstanceID
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		workspaces[item.WorkspaceID] = struct{}{}
		if item.Default {
			workspaceDefault[item.WorkspaceID]++
		}
	}
	for workspaceID := range workspaces {
		if workspaceDefault[workspaceID] != 1 {
			return false
		}
	}
	return true
}

func validTaskBinding(item TaskBindingRecord) bool {
	if !validWorkspaceText(item.TaskID, 128) || !validWorkspaceText(item.InstanceID, 128) || !validWorkspaceText(item.EndpointID, 128) || !validWorkspaceText(item.WorkspaceID, 128) || !validWorkspaceText(item.NativeSessionID, 256) || (item.Ownership != "created" && item.Ownership != "resumed") {
		return false
	}
	switch item.State {
	case RuntimeThreadIdle, RuntimeThreadStarting:
		return item.ActiveRunID == ""
	case RuntimeThreadActive:
		return validWorkspaceText(item.ActiveRunID, 128)
	case RuntimeThreadNeedsReconcile:
		return item.ActiveRunID == "" || validWorkspaceText(item.ActiveRunID, 128)
	default:
		return false
	}
}
