package server

import (
	"testing"

	"github.com/autobrr/distribrr/pkg/scheduler"

	"github.com/stretchr/testify/assert"
)

func Test_summarizeRejections(t *testing.T) {
	assert.Equal(t, "no worker nodes registered", summarizeRejections(nil))

	got := summarizeRejections([]scheduler.NodeRejection{
		{Node: "node1", Reasons: []string{"client qbit: disk_full"}},
		{Node: "node2", Reasons: []string{"node not ready (UNKNOWN)"}},
	})
	assert.Equal(t, "node1 (client qbit: disk_full); node2 (node not ready (UNKNOWN))", got)
}
