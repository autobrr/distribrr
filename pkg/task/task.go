package task

import (
	"time"

	"github.com/google/uuid"
)

type Task struct {
	ID                 uuid.UUID         `json:"id"`
	DownloadURL        string            `json:"download_url"`
	Name               string            `json:"name"`
	Category           string            `json:"category"`
	Tags               string            `json:"tags"`
	Indexer            string            `json:"indexer"`
	State              State             `json:"-"`
	Cpu                float64           `json:"cpu"`
	Memory             int64             `json:"memory"`
	Disk               int64             `json:"disk"`
	SchedulerType      string            `json:"scheduler_type"`
	MaxAllowedReplicas int               `json:"replicas"`
	Labels             map[string]string `json:"labels"`
	Nodes              []string          `json:"nodes"`
	ForceAdd           bool              `json:"force_add"`

	StartTime  time.Time `json:"-"`
	FinishTime time.Time `json:"-"`
}

func NewTask() Task {
	return Task{
		ID:                 uuid.New(),
		DownloadURL:        "",
		Name:               "",
		Category:           "",
		Tags:               "",
		State:              Pending,
		Cpu:                0,
		Memory:             0,
		Disk:               0,
		SchedulerType:      "",
		MaxAllowedReplicas: 0,
		StartTime:          time.Time{},
		FinishTime:         time.Time{},
	}
}

type Event struct {
	ID        uuid.UUID `json:"id"`
	State     State     `json:"state"`
	Timestamp time.Time `json:"timestamp"`
	Task      Task      `json:"task"`
}

func NewEvent() Event {
	return Event{
		ID:        uuid.New(),
		State:     Pending,
		Timestamp: time.Now().UTC(),
		Task:      Task{},
	}
}
