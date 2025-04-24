package raftnode

import (
	"encoding/json"
	"io"
	"log"
	"sync"

	"github.com/hashicorp/raft"
)

type FSM struct {
	mu   sync.Mutex
	data map[string]string
}

func NewFSM() *FSM {
	return &FSM{
		data: make(map[string]string),
	}
}

type Command struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (f *FSM) Apply(logEntry *raft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(logEntry.Data, &cmd); err != nil {
		log.Println("Failed to unmarshal command:", err)
		return nil
	}
	if cmd.Op == "set" {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.data[cmd.Key] = cmd.Value
	}
	return nil
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	data := make(map[string]string)
	for k, v := range f.data {
		data[k] = v
	}

	return &snapshot{store: data}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	data := make(map[string]string)
	if err := json.NewDecoder(rc).Decode(&data); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = data
	return nil
}

type snapshot struct {
	store map[string]string
}

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	b, err := json.Marshal(s.store)
	if err != nil {
		return err
	}

	if _, err := sink.Write(b); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *snapshot) Release() {}

