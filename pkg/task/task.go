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
	Size               uint64            `json:"size"` // torrent size in bytes if known (autobrr {{ .Size }}), 0 if unknown
	SchedulerType      string            `json:"scheduler_type"`
	MaxAllowedReplicas int               `json:"max_replicas"`
	Labels             map[string]string `json:"labels"`
	// Nodes and ForceAdd are accepted but not wired up yet.
	Nodes    []string `json:"nodes"`
	ForceAdd bool     `json:"force_add"`
}

type Event struct {
	ID        uuid.UUID `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Task      Task      `json:"task"`
}

func NewEvent() Event {
	return Event{
		ID:        uuid.New(),
		Timestamp: time.Now().UTC(),
		Task:      Task{},
	}
}
