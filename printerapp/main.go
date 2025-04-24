package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	nodeID := flag.String("id", "node1", "Node ID")
	httpAddr := flag.String("http", "127.0.0.1:8001", "HTTP server address")
	raftAddr := flag.String("raft", "127.0.0.1:9001", "Raft address")
	dataDir := flag.String("data", "./data", "Data directory")
	joinAddr := flag.String("join", "", "Join address (optional)")
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the cluster with this node as the first")
	expectedNodes := flag.Int("nodes", 1, "Expected number of nodes in the cluster")
	flag.Parse()
	fullDataPath := fmt.Sprintf("%s/%s", *dataDir, *nodeID)
	log.Printf("Node: %s | HTTP: %s | Raft: %s | Data: %s | Join: %s | Bootstrap: %v | Expected Nodes: %d",
		*nodeID, *httpAddr, *raftAddr, fullDataPath, *joinAddr, *bootstrap, *expectedNodes)
	store := NewDataStore()
	handlers := NewHandlers(nil, store, *expectedNodes)
	raftNode, err := setupRaft(*nodeID, *raftAddr, fullDataPath, store)
	if err != nil {
		log.Fatalf("Failed to setup Raft: %v", err)
	}
	handlers.raft = raftNode
	if *bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:       raft.ServerID(*nodeID),
					Address:  raft.ServerAddress(*raftAddr),
					Suffrage: raft.Voter,
				},
			},
		}
		raftNode.BootstrapCluster(configuration)
		log.Printf("Bootstrapped cluster with node %s", *nodeID)
		if *expectedNodes > 1 {
			log.Printf("Waiting for %d nodes to join before starting normal operations", *expectedNodes-1)
			go func() {
				handlers.clusterReady.Wait()
				log.Printf("All expected nodes have joined the cluster")
			}()
		}
	} else if *joinAddr != "" {
		var success bool
		for i := 0; i < 10; i++ {
			req := map[string]string{"id": *nodeID, "addr": *raftAddr}
			data, _ := json.Marshal(req)
			resp, err := http.Post(fmt.Sprintf("http://%s/join", *joinAddr), "application/json", bytes.NewBuffer(data))
			if err == nil && resp.StatusCode == http.StatusOK {
				log.Printf("Successfully joined cluster via %s", *joinAddr)
				success = true
				break
			}
			log.Printf("Join attempt %d failed: %v", i+1, err)
			time.Sleep(2 * time.Second)
		}
		if !success {
			log.Fatalf("Failed to join cluster after multiple attempts")
		}
	} else {
		log.Printf("Running without bootstrap or join. This node will wait for manual configuration.")
	}

	// Printer endpoints
	http.HandleFunc("/api/v1/printers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handlers.ListPrintersHandler(w, r)
		} else if r.Method == http.MethodPost {
			handlers.CreatePrinterHandler(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Filament endpoints
	http.HandleFunc("/api/v1/filaments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handlers.ListFilamentsHandler(w, r)
		} else if r.Method == http.MethodPost {
			handlers.CreateFilamentHandler(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Stop printer endpoint
	http.HandleFunc("/api/v1/printers/stop", handlers.StopPrinterHandler)

	// Print Job endpoints
	http.HandleFunc("/api/v1/print_jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handlers.ListPrintJobsHandler(w, r)
		} else if r.Method == http.MethodPost {
			handlers.CreatePrintJobHandler(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Print Job status update endpoint
	http.HandleFunc("/api/v1/print_jobs/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/status") {
			handlers.UpdatePrintJobStatusHandler(w, r)
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})

	// Cluster endpoints
	http.HandleFunc("/join", handlers.JoinHandler)
	http.HandleFunc("/status", handlers.StatusHandler)

	log.Printf("HTTP server listening at %s", *httpAddr)
	log.Fatal(http.ListenAndServe(*httpAddr, nil))
}



