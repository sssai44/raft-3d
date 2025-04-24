package main

import (
	"log"
	"os"
	"raft3d/raftnode"
)

func main() {
	if len(os.Args) < 4 {
		log.Fatalf("Usage: %s <nodeID> <bindAddr> <peer1,peer2,...>", os.Args[0])
	}

	nodeID := os.Args[1]
	bindAddr := os.Args[2]
	peers := os.Args[3:]

	dataDir := "./data-" + nodeID

	_, err := raftnode.NewRaftNode(nodeID, bindAddr, dataDir, peers)
	if err != nil {
		log.Fatalf("Failed to create Raft node: %v", err)
	}

	log.Printf("Raft node %s started at %s", nodeID, bindAddr)

	select {} // keep running
}

