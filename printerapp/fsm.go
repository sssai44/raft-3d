package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

type FSM struct {
	store *DataStore
	mu    sync.Mutex
}

// ... (copy the entire FSM struct methods here, including Apply, Snapshot, Restore, and snapshot struct)
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


