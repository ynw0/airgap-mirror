package domain

import "time"

// ExportJob is durable server-side state, independent of the HTTP request.
type ExportJob struct {
	ID               string        `json:"id"`
	SourceIDs        []string      `json:"sourceIds"`
	RepairJobID      string        `json:"repairJobId,omitempty"`
	Status           string        `json:"status"`
	Stage            string        `json:"stage"`
	CompletedSources int           `json:"completedSources"`
	TotalSources     int           `json:"totalSources"`
	Capsule          *StateCapsule `json:"capsule,omitempty"`
	Name             string        `json:"name,omitempty"`
	Download         string        `json:"download,omitempty"`
	ErrorText        string        `json:"errorText,omitempty"`
	CreatedAt        time.Time     `json:"createdAt"`
	UpdatedAt        time.Time     `json:"updatedAt"`
}
