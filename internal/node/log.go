package node

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const maxOperationalLogBytes = 1 << 20

type operationalLog struct {
	mu   sync.Mutex
	path string
}

type logRecord struct {
	Time  string `json:"time"`
	Event string `json:"event"`
	State string `json:"state,omitempty"`
	Count int    `json:"count,omitempty"`
}

func newOperationalLog(path string) *operationalLog { return &operationalLog{path: path} }

func (l *operationalLog) write(event, state string, count int) {
	if l == nil || !safeLogValue(event) || !safeLogValue(state) {
		return
	}
	record, err := json.Marshal(logRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Event: event, State: state, Count: count})
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(record)+1) > maxOperationalLogBytes {
		_ = os.Remove(l.path + ".1")
		_ = os.Rename(l.path, l.path+".1")
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(record, '\n'))
	_ = file.Sync()
	_ = file.Close()
}

func safeLogValue(value string) bool {
	if len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character == '_' || character == '-' || character == '.' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return value != ""
}
