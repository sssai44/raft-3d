package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
)

// Printer represents a printer in our system
type Printer struct {
	ID      string `json:"id"`
	Company string `json:"company"`
	Model   string `json:"model"`
	Status  string `json:"status,omitempty"` // Added for stop functionality
}

// Filament represents a filament in the system
type Filament struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Color  string  `json:"color"`
	Weight float64 `json:"weight"`
}

// PrintJob represents a print job in the system
type PrintJob struct {
	ID               string  `json:"id"`
	PrinterID        string  `json:"printer_id"`
	FilamentID       string  `json:"filament_id"`
	PrintWeightGrams float64 `json:"print_weight_in_grams"`
	Status           string  `json:"status"` // "queued", "running", "done", "canceled"
	CreatedAt        int64   `json:"created_at"`
}

// Command represents an operation to perform on our state machine
type Command struct {
	Op       string   `json:"op"`
	Printer  Printer  `json:"printer,omitempty"`
	Filament Filament `json:"filament,omitempty"`
	PrintJob PrintJob `json:"print_job,omitempty"`
	TargetID string   `json:"target_id,omitempty"` // For stop_printer and update_print_job_status
	Status   string   `json:"status,omitempty"`    // For status update
}

// FSM is our finite state machine that implements raft.FSM interface
type FSM struct {
	store *DataStore
	mu    sync.Mutex
}

// DataStore holds the current state
type DataStore struct {
	printers  map[string]Printer
	filaments map[string]Filament
	printJobs map[string]PrintJob
	mu        sync.RWMutex
}

func NewDataStore() *DataStore {
	return &DataStore{
		printers:  make(map[string]Printer),
		filaments: make(map[string]Filament),
		printJobs: make(map[string]PrintJob),
	}
}

// Apply implements the raft.FSM interface
func (f *FSM) Apply(log *raft.Log) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	var cmd Command
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal command: %s", err)
	}

	switch cmd.Op {
	case "create":
		f.store.mu.Lock()
		f.store.printers[cmd.Printer.ID] = cmd.Printer
		f.store.mu.Unlock()
		return cmd.Printer
	case "create_filament":
		f.store.mu.Lock()
		f.store.filaments[cmd.Filament.ID] = cmd.Filament
		f.store.mu.Unlock()
		return cmd.Filament
	case "stop_printer":
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		p, ok := f.store.printers[cmd.TargetID]
		if !ok {
			return fmt.Errorf("printer not found")
		}
		p.Status = "stopped"
		f.store.printers[cmd.TargetID] = p
		return p
	case "create_print_job":
		f.store.mu.Lock()
		defer f.store.mu.Unlock()

		// Validate printer and filament exist
		_, printerExists := f.store.printers[cmd.PrintJob.PrinterID]
		if !printerExists {
			return fmt.Errorf("printer not found")
		}

		filament, filamentExists := f.store.filaments[cmd.PrintJob.FilamentID]
		if !filamentExists {
			return fmt.Errorf("filament not found")
		}

		// Calculate current weight used by queued/running jobs for this filament
		var usedWeight float64
		for _, job := range f.store.printJobs {
			if job.FilamentID == cmd.PrintJob.FilamentID && (job.Status == "queued" || job.Status == "running") {
				usedWeight += job.PrintWeightGrams
			}
		}

		// Check if there's enough filament remaining
		if (filament.Weight - usedWeight - cmd.PrintJob.PrintWeightGrams) < 0 {
			return fmt.Errorf("not enough filament remaining")
		}

		// Initialize job with queued status
		cmd.PrintJob.Status = "queued"
		if cmd.PrintJob.CreatedAt == 0 {
			cmd.PrintJob.CreatedAt = time.Now().Unix()
		}

		f.store.printJobs[cmd.PrintJob.ID] = cmd.PrintJob
		return cmd.PrintJob

	case "update_print_job_status":
		f.store.mu.Lock()
		defer f.store.mu.Unlock()

		job, ok := f.store.printJobs[cmd.TargetID]
		if !ok {
			return fmt.Errorf("print job not found")
		}

		// Validate status transitions
		switch cmd.Status {
		case "running":
			if job.Status != "queued" {
				return fmt.Errorf("job can only transition to running from queued state")
			}
		case "done":
			if job.Status != "running" {
				return fmt.Errorf("job can only transition to done from running state")
			}

			// Reduce filament weight when job completes
			filament, ok := f.store.filaments[job.FilamentID]
			if ok {
				filament.Weight -= job.PrintWeightGrams
				f.store.filaments[job.FilamentID] = filament
			}
		case "canceled":
			if job.Status != "queued" && job.Status != "running" {
				return fmt.Errorf("job can only be canceled from queued or running state")
			}
		default:
			return fmt.Errorf("invalid status: %s", cmd.Status)
		}

		job.Status = cmd.Status
		f.store.printJobs[cmd.TargetID] = job
		return job

	default:
		return fmt.Errorf("unknown command: %s", cmd.Op)
	}
}

// Snapshot implements the raft.FSM interface
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.store.mu.RLock()
	defer f.store.mu.RUnlock()

	// Create a copy of the printers, filaments, and printJobs maps
	printers := make(map[string]Printer)
	for k, v := range f.store.printers {
		printers[k] = v
	}
	filaments := make(map[string]Filament)
	for k, v := range f.store.filaments {
		filaments[k] = v
	}
	printJobs := make(map[string]PrintJob)
	for k, v := range f.store.printJobs {
		printJobs[k] = v
	}

	return &snapshot{
		printers:  printers,
		filaments: filaments,
		printJobs: printJobs,
	}, nil
}

// Restore implements the raft.FSM interface
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var data struct {
		Printers  map[string]Printer  `json:"printers"`
		Filaments map[string]Filament `json:"filaments"`
		PrintJobs map[string]PrintJob `json:"print_jobs"`
	}
	if err := json.NewDecoder(rc).Decode(&data); err != nil {
		return err
	}
	f.store.mu.Lock()
	f.store.printers = data.Printers
	f.store.filaments = data.Filaments
	f.store.printJobs = data.PrintJobs
	f.store.mu.Unlock()
	return nil
}

// snapshot implements the raft.FSMSnapshot interface
type snapshot struct {
	printers  map[string]Printer
	filaments map[string]Filament
	printJobs map[string]PrintJob
}

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	err := json.NewEncoder(sink).Encode(struct {
		Printers  map[string]Printer  `json:"printers"`
		Filaments map[string]Filament `json:"filaments"`
		PrintJobs map[string]PrintJob `json:"print_jobs"`
	}{
		Printers:  s.printers,
		Filaments: s.filaments,
		PrintJobs: s.printJobs,
	})
	if err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *snapshot) Release() {}

// Handlers encapsulates HTTP handlers
type Handlers struct {
	raft          *raft.Raft
	store         *DataStore
	clusterReady  *sync.WaitGroup // WaitGroup for cluster readiness
	expectedNodes int             // Number of expected nodes in cluster
}

func NewHandlers(r *raft.Raft, s *DataStore, expectedNodes int) *Handlers {
	wg := &sync.WaitGroup{}
	if expectedNodes > 0 {
		wg.Add(expectedNodes - 1) // Subtract 1 for the current node
	}
	return &Handlers{
		raft:          r,
		store:         s,
		clusterReady:  wg,
		expectedNodes: expectedNodes,
	}
}

func (h *Handlers) CreatePrinterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var p Printer
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.ID == "" {
		p.ID = uuid.New().String()
	}

	if h.raft.State() != raft.Leader {
		leaderAddr := string(h.raft.Leader())
		if leaderAddr == "" {
			http.Error(w, "No leader available", http.StatusServiceUnavailable)
			return
		}
		leaderPortStr := leaderAddr[strings.LastIndex(leaderAddr, ":")+1:]
		leaderPort, err := strconv.Atoi(leaderPortStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid leader address: %s", leaderAddr), http.StatusInternalServerError)
			return
		}
		httpPort := leaderPort - 1000
		leaderHost := leaderAddr[:strings.LastIndex(leaderAddr, ":")]
		leaderHTTPAddr := fmt.Sprintf("http://%s:%d/api/v1/printers", leaderHost, httpPort)
		resp, err := http.Post(leaderHTTPAddr, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to forward to leader: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read leader response: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(resp.StatusCode)
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Write(respBody)
		return
	}

	cmd := Command{
		Op:      "create",
		Printer: p,
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f := h.raft.Apply(b, 5*time.Second)
	if err := f.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result := f.Response()
	if err, ok := result.(error); ok {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *Handlers) ListPrintersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	var list []Printer
	for _, p := range h.store.printers {
		list = append(list, p)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// === Filament and Stop Printer Handlers ===

func (h *Handlers) CreateFilamentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var f Filament
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	cmd := Command{
		Op:       "create_filament",
		Filament: f,
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	future := h.raft.Apply(b, 5*time.Second)
	if err := future.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(f)
}

func (h *Handlers) ListFilamentsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	var list []Filament
	for _, f := range h.store.filaments {
		list = append(list, f)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *Handlers) StopPrinterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PrinterID string `json:"printer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	cmd := Command{
		Op:       "stop_printer",
		TargetID: req.PrinterID,
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	future := h.raft.Apply(b, 5*time.Second)
	if err := future.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(future.Response())
}

// === Print Job Handlers ===

func (h *Handlers) CreatePrintJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var pj PrintJob
	if err := json.NewDecoder(r.Body).Decode(&pj); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if pj.ID == "" {
		pj.ID = uuid.New().String()
	}

	// Only validate printer/filament existence - the Raft FSM will handle the rest
	h.store.mu.RLock()
	_, printerExists := h.store.printers[pj.PrinterID]
	_, filamentExists := h.store.filaments[pj.FilamentID]
	h.store.mu.RUnlock()

	if !printerExists {
		http.Error(w, "Invalid printer_id", http.StatusBadRequest)
		return
	}

	if !filamentExists {
		http.Error(w, "Invalid filament_id", http.StatusBadRequest)
		return
	}

	// Forward to leader if needed
	if h.raft.State() != raft.Leader {
		leaderAddr := string(h.raft.Leader())
		if leaderAddr == "" {
			http.Error(w, "No leader available", http.StatusServiceUnavailable)
			return
		}

		leaderPortStr := leaderAddr[strings.LastIndex(leaderAddr, ":")+1:]
		leaderPort, err := strconv.Atoi(leaderPortStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid leader address: %s", leaderAddr), http.StatusInternalServerError)
			return
		}

		httpPort := leaderPort - 1000
		leaderHost := leaderAddr[:strings.LastIndex(leaderAddr, ":")]
		leaderHTTPAddr := fmt.Sprintf("http://%s:%d/api/v1/print_jobs", leaderHost, httpPort)

		reqBody, _ := json.Marshal(pj)
		resp, err := http.Post(leaderHTTPAddr, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to forward to leader: %v", err), http.StatusInternalServerError)
			return
		}

		defer resp.Body.Close()
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read leader response: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(resp.StatusCode)
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Write(respBody)
		return
	}

	cmd := Command{
		Op:       "create_print_job",
		PrintJob: pj,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	f := h.raft.Apply(b, 5*time.Second)
	if err := f.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := f.Response()
	if err, ok := result.(error); ok {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handlers) ListPrintJobsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.store.mu.RLock()
	defer h.store.mu.RUnlock()

	var list []PrintJob
	statusFilter := r.URL.Query().Get("status")

	for _, pj := range h.store.printJobs {
		if statusFilter != "" && pj.Status != statusFilter {
			continue
		}
		list = append(list, pj)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *Handlers) UpdatePrintJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from URL
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 5 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	jobID := pathParts[4]

	var req struct {
		Status string `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate status
	if req.Status != "running" && req.Status != "done" && req.Status != "canceled" {
		http.Error(w, "Invalid status. Must be one of: running, done, canceled", http.StatusBadRequest)
		return
	}

	// Forward to leader if needed
	if h.raft.State() != raft.Leader {
		leaderAddr := string(h.raft.Leader())
		if leaderAddr == "" {
			http.Error(w, "No leader available", http.StatusServiceUnavailable)
			return
		}

		leaderPortStr := leaderAddr[strings.LastIndex(leaderAddr, ":")+1:]
		leaderPort, err := strconv.Atoi(leaderPortStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid leader address: %s", leaderAddr), http.StatusInternalServerError)
			return
		}

		httpPort := leaderPort - 1000
		leaderHost := leaderAddr[:strings.LastIndex(leaderAddr, ":")]
		leaderHTTPAddr := fmt.Sprintf("http://%s:%d/api/v1/print_jobs/%s/status", leaderHost, httpPort, jobID)

		reqBody, _ := json.Marshal(req)
		resp, err := http.Post(leaderHTTPAddr, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to forward to leader: %v", err), http.StatusInternalServerError)
			return
		}

		defer resp.Body.Close()
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read leader response: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(resp.StatusCode)
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Write(respBody)
		return
	}

	cmd := Command{
		Op:       "update_print_job_status",
		TargetID: jobID,
		Status:   req.Status,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	f := h.raft.Apply(b, 5*time.Second)
	if err := f.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := f.Response()
	if err, ok := result.(error); ok {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// ===============================================

func (h *Handlers) JoinHandler(w http.ResponseWriter, r *http.Request) {
	if h.raft == nil {
		http.Error(w, "Raft not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		future := h.raft.GetConfiguration()
		if err := future.Error(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(future.Configuration())
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			ID   string `json:"id"`
			Addr string `json:"addr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		log.Printf("Join request for node %s at %s", req.ID, req.Addr)
		f := h.raft.AddVoter(raft.ServerID(req.ID), raft.ServerAddress(req.Addr), 0, 0)
		if err := f.Error(); err != nil {
			http.Error(w, fmt.Sprintf("Failed to add voter: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("Added node %s at %s to the cluster", req.ID, req.Addr)
		if h.clusterReady != nil {
			h.clusterReady.Done()
			log.Printf("🔄 %d more nodes needed before election can start", h.expectedNodes-1-getClusterSize(h.raft)+1)
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handlers) StatusHandler(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"state":        h.raft.State().String(),
		"leader":       string(h.raft.Leader()),
		"lastLogIndex": h.raft.LastIndex(),
		"appliedIndex": h.raft.AppliedIndex(),
	}
	f := h.raft.GetConfiguration()
	if err := f.Error(); err == nil {
		status["cluster"] = f.Configuration().Servers
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// Helper function to get the current cluster size
func getClusterSize(r *raft.Raft) int {
	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return 0
	}
	return len(future.Configuration().Servers)
}

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
