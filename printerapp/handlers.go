package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
)

// ... (copy the Handlers struct, NewHandlers, and all handler methods here)
// Handlers encapsulates HTTP handlers
type Handlers struct {
	raft          *raft.Raft
	store         *DataStore
	clusterReady  *sync.WaitGroup // WaitGroup for cluster readiness
	expectedNodes int             // Number of expected nodes in cluster
}

// Helper function to get the current cluster size
func getClusterSize(r *raft.Raft) int {
    future := r.GetConfiguration()
    if err := future.Error(); err != nil {
        return 0
    }
    return len(future.Configuration().Servers)
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

