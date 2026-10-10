package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/autobrr/distribrr/pkg/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_summarizeRejections(t *testing.T) {
	assert.Equal(t, "no worker nodes registered", summarizeRejections(nil))

	got := summarizeRejections([]scheduler.NodeRejection{
		{Node: "node1", Reasons: []string{"client qbit: disk_full"}},
		{Node: "node2", Reasons: []string{"node not ready (UNKNOWN)"}},
	})
	assert.Equal(t, "node1 (client qbit: disk_full); node2 (node not ready (UNKNOWN))", got)
}

func TestAPIServer_Open_portInUse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()

	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)

	cfg := NewConfig()
	cfg.Http.Host = "127.0.0.1"
	cfg.Http.Port = port

	srv := NewAPIServer(cfg, NewService(cfg))

	assert.Error(t, srv.Open())
}

func TestConfig_WriteToFile_error(t *testing.T) {
	cfg := NewConfig()
	cfg.configFile = filepath.Join(t.TempDir(), "missing", "config.yaml")

	assert.Error(t, cfg.WriteToFile())
}

func TestService_removeNodeFromConfig(t *testing.T) {
	cfg := NewConfig()
	cfg.configFile = filepath.Join(t.TempDir(), "config.yaml")
	cfg.Nodes = []*AgentNode{
		{Name: "node1", Addr: "http://node1:7430"},
		{Name: "node2", Addr: "http://node2:7430"},
	}

	s := NewService(cfg)
	require.NoError(t, s.removeNodeFromConfig(t.Context(), "node1"))

	require.Len(t, cfg.Nodes, 1)
	assert.Equal(t, "node2", cfg.Nodes[0].Name)

	data, err := os.ReadFile(cfg.configFile)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "node1")
	assert.Contains(t, string(data), "node2")
}

func TestAPIServer_Shutdown(t *testing.T) {
	cfg := NewConfig()
	cfg.Http.Host = "127.0.0.1"
	cfg.Http.Port = "0"

	srv := NewAPIServer(cfg, NewService(cfg))

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Open()
	}()

	require.NoError(t, srv.Shutdown(t.Context()))

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not return after Shutdown")
	}
}
