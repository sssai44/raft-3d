package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
)

func setupRaft(nodeID string, raftAddr string, dataDir string, store *DataStore) (*raft.Raft, error) {
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	config.ElectionTimeout = 1 * time.Second
	config.HeartbeatTimeout = 500 * time.Millisecond
	config.CommitTimeout = 500 * time.Millisecond

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	boltDB, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
	if err != nil {
		return nil, err
	}
	logStore := boltDB
	snapshotStore, err := raft.NewFileSnapshotStore(dataDir, 3, os.Stderr)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(raftAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}
	fsm := &FSM{store: store}
	r, err := raft.NewRaft(config, fsm, logStore, boltDB, snapshotStore, transport)
	if err != nil {
		return nil, err
	}
	return r, nil
}

