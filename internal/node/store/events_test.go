package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var testEventBinding = EventBinding{OwnerID: "owner-test", NodeID: "node-test", StreamID: "node-events-v1"}

func TestEventAppendReplayAcknowledgeAndRestart(t *testing.T) {
	database, path := openTestStore(t)
	retention := EventRetention{MaxAge: 24 * time.Hour, MaxBytes: 16 << 20}
	for index := 1; index <= 3; index++ {
		index := index
		record, err := database.AppendEvent(context.Background(), testEventBinding, EventTarget{ThreadID: "thread-test"}, "runtime.status", fixedNow.Add(time.Duration(index)*time.Second), retention, func(sequence int64) (string, []byte, error) {
			return fmt.Sprintf("event-%d", index), []byte(fmt.Sprintf(`{"sequence":%d}`, sequence)), nil
		})
		if err != nil || record.Sequence != int64(index) {
			t.Fatalf("AppendEvent(%d) = %#v, %v", index, record, err)
		}
	}
	replayed, head, err := database.ReplayEvents(context.Background(), testEventBinding, 1, 10)
	if err != nil || len(replayed) != 2 || replayed[0].Sequence != 2 || head.LatestSequence != 3 || head.EarliestSequence != 1 {
		t.Fatalf("ReplayEvents = %#v, %#v, %v", replayed, head, err)
	}
	replayed[0].Frame[0] = 'x'
	again, _, err := database.ReplayEvents(context.Background(), testEventBinding, 1, 1)
	if err != nil || again[0].Frame[0] == 'x' {
		t.Fatal("replay frame ownership was not isolated")
	}
	if err := database.AcknowledgeEvents(context.Background(), testEventBinding, 2); err != nil {
		t.Fatal(err)
	}
	if err := database.AcknowledgeEvents(context.Background(), testEventBinding, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("cursor rollback error = %v", err)
	}
	pending, err := database.Pending(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].Sequence != 3 {
		t.Fatalf("Pending after cursor = %#v, %v", pending, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	head, err = reopened.EventHead(context.Background(), testEventBinding)
	if err != nil || head.LatestSequence != 3 {
		t.Fatalf("reopened head = %#v, %v", head, err)
	}
}

func TestEventSequenceConcurrentAndRetentionBounded(t *testing.T) {
	database, _ := openTestStore(t)
	retention := EventRetention{MaxAge: 24 * time.Hour, MaxBytes: 16 << 20}
	const count = 32
	var wait sync.WaitGroup
	errorsCh := make(chan error, count)
	for index := 0; index < count; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := database.AppendEvent(context.Background(), testEventBinding, EventTarget{}, "runtime.status", fixedNow, retention, func(sequence int64) (string, []byte, error) {
				return fmt.Sprintf("parallel-%d", index), []byte(fmt.Sprintf("frame-%d", sequence)), nil
			})
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, head, err := database.ReplayEvents(context.Background(), testEventBinding, 0, count)
	if err != nil || len(records) != count || head.LatestSequence != count {
		t.Fatalf("concurrent replay = %d, %#v, %v", len(records), head, err)
	}
	for index, record := range records {
		if record.Sequence != int64(index+1) {
			t.Fatalf("sequence[%d] = %d", index, record.Sequence)
		}
	}

	largeBinding := EventBinding{OwnerID: "owner-test", NodeID: "node-test", StreamID: "bounded-events"}
	largeRetention := EventRetention{MaxAge: 24 * time.Hour, MaxBytes: 1 << 20}
	frame := bytes.Repeat([]byte{'z'}, 600<<10)
	for index := 0; index < 2; index++ {
		index := index
		if _, err := database.AppendEvent(context.Background(), largeBinding, EventTarget{}, "runtime.status", fixedNow, largeRetention, func(sequence int64) (string, []byte, error) {
			return fmt.Sprintf("large-%d", index), frame, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	retained, boundedHead, err := database.ReplayEvents(context.Background(), largeBinding, 0, 10)
	if err != nil || len(retained) != 1 || retained[0].Sequence != 2 || boundedHead.EarliestSequence != 2 || boundedHead.LatestSequence != 2 {
		t.Fatalf("bounded retention = %d, %#v, %v", len(retained), boundedHead, err)
	}
}

func TestSnapshotAndControlStateAreStrictAndDetached(t *testing.T) {
	database, _ := openTestStore(t)
	payload := []byte(`{"status":"active"}`)
	snapshot := SnapshotRecord{WorkspaceID: "workspace", ThreadID: "thread", Status: "active", LatestSequence: 7, Payload: payload}
	if err := database.SaveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'x'
	loaded, err := database.Snapshot(context.Background(), "thread")
	if err != nil || loaded.Payload[0] == 'x' {
		t.Fatalf("Snapshot = %#v, %v", loaded, err)
	}

	digest := bytes.Repeat([]byte{0x2a}, 32)
	control, err := database.CreateControl(context.Background(), ControlRecord{MessageID: "control", RequestDigest: digest, Type: "turn.start", EventTarget: EventTarget{WorkspaceID: "workspace", ThreadID: "thread"}, State: ControlValidated})
	if err != nil || control.State != ControlValidated {
		t.Fatalf("CreateControl = %#v, %v", control, err)
	}
	if _, err := database.TransitionControl(context.Background(), "control", ControlDispatching, "", "", 0); err != nil {
		t.Fatal(err)
	}
	terminal, err := database.TransitionControl(context.Background(), "control", ControlAmbiguous, "ambiguous", "node-events-v1", 8)
	if err != nil || terminal.State != ControlAmbiguous {
		t.Fatalf("terminal control = %#v, %v", terminal, err)
	}
	if _, err := database.TransitionControl(context.Background(), "control", ControlConfirmed, "", "node-events-v1", 9); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal mutation error = %v", err)
	}
	conflictDigest := bytes.Repeat([]byte{0x7f}, 32)
	if _, err := database.CreateControl(context.Background(), ControlRecord{MessageID: "control", RequestDigest: conflictDigest, Type: "turn.start", EventTarget: EventTarget{WorkspaceID: "workspace", ThreadID: "thread"}, State: ControlValidated}); !errors.Is(err, ErrConflict) {
		t.Fatalf("digest collision error = %v", err)
	}
}

func TestEventRecoveryMigrationUpgradesSchemaV3(t *testing.T) {
	original := nodeMigrations
	nodeMigrations = append([]migration(nil), original[:3]...)
	path := filepath.Join(t.TempDir(), "node-v3.db")
	versionThree, err := Open(context.Background(), path, Options{})
	if err != nil {
		nodeMigrations = original
		t.Fatal(err)
	}
	if err := versionThree.SaveRuntimeThread(context.Background(), RuntimeThreadRecord{Adapter: "codex", ThreadID: "thread", WorkspaceID: "workspace", Ownership: "created", State: RuntimeThreadNeedsReconcile}); err != nil {
		nodeMigrations = original
		t.Fatal(err)
	}
	_ = versionThree.Close()
	nodeMigrations = original
	upgraded, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("upgraded version = %d, %v", version, err)
	}
	if record, err := upgraded.RuntimeThread(context.Background(), "thread"); err != nil || record.State != RuntimeThreadNeedsReconcile {
		t.Fatalf("runtime ownership after upgrade = %#v, %v", record, err)
	}
	head, err := upgraded.EventHead(context.Background(), testEventBinding)
	if err != nil || head != (EventHead{}) {
		t.Fatalf("empty event head = %#v, %v", head, err)
	}
}

func TestControlResultEventRollsBackAsOneTransaction(t *testing.T) {
	database, _ := openTestStore(t)
	digest := bytes.Repeat([]byte{0x44}, 32)
	if _, err := database.CreateControl(context.Background(), ControlRecord{MessageID: "atomic-control", RequestDigest: digest, Type: "turn.start", State: ControlValidated}); err != nil {
		t.Fatal(err)
	}
	appendResult := func() (EventRecord, error) {
		return database.AppendControlEvent(context.Background(), testEventBinding, EventTarget{}, "control.result", fixedNow, EventRetention{MaxAge: time.Hour, MaxBytes: 16 << 20}, ControlEventMutation{MessageID: "atomic-control", State: ControlAmbiguous, ErrorCode: "ambiguous"}, func(sequence int64) (string, []byte, error) {
			return "atomic-result", []byte(fmt.Sprintf(`{"sequence":%d}`, sequence)), nil
		})
	}
	if _, err := appendResult(); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-dispatching append error = %v", err)
	}
	head, err := database.EventHead(context.Background(), testEventBinding)
	if err != nil || head != (EventHead{}) {
		t.Fatalf("rolled-back event head = %#v, %v", head, err)
	}
	if _, err := database.TransitionControl(context.Background(), "atomic-control", ControlDispatching, "", "", 0); err != nil {
		t.Fatal(err)
	}
	record, err := appendResult()
	if err != nil || record.Sequence != 1 {
		t.Fatalf("atomic result = %#v, %v", record, err)
	}
	control, err := database.Control(context.Background(), "atomic-control")
	if err != nil || control.State != ControlAmbiguous || control.ResultSequence != record.Sequence {
		t.Fatalf("atomic control = %#v, %v", control, err)
	}
	pending, err := database.Pending(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].MessageID != record.MessageID {
		t.Fatalf("atomic outbox = %#v, %v", pending, err)
	}
}
