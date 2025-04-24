package main

type Printer struct {
	ID      string `json:"id"`
	Company string `json:"company"`
	Model   string `json:"model"`
	Status  string `json:"status,omitempty"`
}

type Filament struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Color  string  `json:"color"`
	Weight float64 `json:"weight"`
}

type PrintJob struct {
	ID               string  `json:"id"`
	PrinterID        string  `json:"printer_id"`
	FilamentID       string  `json:"filament_id"`
	PrintWeightGrams float64 `json:"print_weight_in_grams"`
	Status           string  `json:"status"`
	CreatedAt        int64   `json:"created_at"`
}

type Command struct {
	Op       string   `json:"op"`
	Printer  Printer  `json:"printer,omitempty"`
	Filament Filament `json:"filament,omitempty"`
	PrintJob PrintJob `json:"print_job,omitempty"`
	TargetID string   `json:"target_id,omitempty"`
	Status   string   `json:"status,omitempty"`
}

