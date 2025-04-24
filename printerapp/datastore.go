package main

import "sync"

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

