package raftnode

import (
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	"github.com/hashicorp/raft-boltdb"
)

type RaftNode struct {
	Raft *raft.Raft
}

func NewRaftNode(id, bindAddr, dataDir string, peers []string) (*RaftNode, error) {
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(id)

	addr, err := net.ResolveTCPAddr("tcp", bindAddr)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(bindAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}

	snapshots, err := raft.NewFileSnapshotStore(dataDir, 1, os.Stderr)
	if err != nil {
		return nil, err
	}

	logStorePath := filepath.Join(dataDir, "raft-log.bolt")
	logStore, err := raftboltdb.NewBoltStore(logStorePath)
	if err != nil {
		return nil, err
	}

	fsm := NewFSM()

	node, err := raft.NewRaft(config, fsm, logStore, logStore, snapshots, transport)
	if err != nil {
		return nil, err
	}

	rn := &RaftNode{Raft: node}

	servers := []raft.Server{}
	for _, peer := range peers {
		servers = append(servers, raft.Server{
			ID:      raft.ServerID(peer),
			Address: raft.ServerAddress(peer),
		})
	}

	configFuture := rn.Raft.BootstrapCluster(raft.Configuration{Servers: servers})
	if err := configFuture.Error(); err != nil {
		return nil, err
	}

	return rn, nil
}

