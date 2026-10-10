package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yml "gopkg.in/yaml.v3"
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

// fakeAgent serves the agent's verify endpoint and accepts token.
func fakeAgent(t *testing.T, token string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/verify" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func newTestService(t *testing.T, configFile string) *Service {
	t.Helper()

	cfg := NewConfig()
	cfg.Http.Token = "server-token"
	cfg.configFile = configFile

	return NewService(cfg)
}

func registerRequest(name, addr, token string) RegisterRequest {
	return RegisterRequest{
		NodeName:    name,
		ClientAddr:  addr,
		AgentToken:  token,
		Labels:      map[string]string{"region": "eu"},
		ServerToken: "server-token",
	}
}

func readConfigNodes(t *testing.T, path string) []*AgentNode {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg struct {
		Nodes []*AgentNode `yaml:"nodes"`
	}
	require.NoError(t, yml.Unmarshal(data, &cfg))

	return cfg.Nodes
}

func TestService_OnRegister_newNode(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "agent-token")))

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, "node1", nodes[0].Name)
	assert.Equal(t, agentSrv.URL, nodes[0].Addr())
	assert.Equal(t, map[string]string{"region": "eu"}, nodes[0].Labels())

	assert.Equal(t, []*AgentNode{{Name: "node1", Addr: agentSrv.URL, Token: "agent-token"}}, s.cfg.Nodes)
	assert.Equal(t, s.cfg.Nodes, readConfigNodes(t, configFile))
}

func TestService_OnRegister_badAgentToken(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	assert.Error(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "wrong-token")))
	assert.Empty(t, s.GetNodes())
	assert.Empty(t, s.cfg.Nodes)
	assert.NoFileExists(t, configFile)
}

func TestService_OnRegister_updatesExistingNode(t *testing.T) {
	oldAgent := fakeAgent(t, "old-token")
	newAgent := fakeAgent(t, "new-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", oldAgent.URL, "old-token")))
	s.GetNodes()[0].SetStatus(node.StatusUnknown)

	req := registerRequest("node1", newAgent.URL, "new-token")
	req.Labels = map[string]string{"region": "us"}
	require.NoError(t, s.OnRegister(t.Context(), req))

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, newAgent.URL, nodes[0].Addr())
	assert.Equal(t, "new-token", nodes[0].Token())
	assert.Equal(t, map[string]string{"region": "us"}, nodes[0].Labels())
	assert.Equal(t, node.Status(node.StatusReady), nodes[0].Status())

	// the rebuilt agent client talks to the new agent with the new token
	require.NoError(t, nodes[0].VerifyToken(t.Context()))

	want := []*AgentNode{{Name: "node1", Addr: newAgent.URL, Token: "new-token"}}
	assert.Equal(t, want, s.cfg.Nodes)
	assert.Equal(t, want, readConfigNodes(t, configFile))
}

func TestService_OnRegister_removedNode(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "agent-token")))
	require.NoError(t, s.Deregister(t.Context(), DeregisterRequest{NodeName: "node1"}))
	require.Equal(t, node.Status(node.StatusRemoved), s.GetNodes()[0].Status())

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "agent-token")))

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, node.Status(node.StatusReady), nodes[0].Status())
	assert.Len(t, s.cfg.Nodes, 1)
	assert.Len(t, readConfigNodes(t, configFile), 1)
}

func TestService_OnRegister_writeError(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	s := newTestService(t, filepath.Join(t.TempDir(), "missing", "config.yaml"))

	assert.Error(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "agent-token")))
	assert.Empty(t, s.GetNodes())
	assert.Empty(t, s.cfg.Nodes)
}

func TestService_OnRegister_writeErrorOnUpdate(t *testing.T) {
	oldAgent := fakeAgent(t, "old-token")
	newAgent := fakeAgent(t, "new-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", oldAgent.URL, "old-token")))

	s.cfg.configFile = filepath.Join(t.TempDir(), "missing", "config.yaml")
	assert.Error(t, s.OnRegister(t.Context(), registerRequest("node1", newAgent.URL, "new-token")))

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, oldAgent.URL, nodes[0].Addr())
	assert.Equal(t, "old-token", nodes[0].Token())
	assert.Equal(t, []*AgentNode{{Name: "node1", Addr: oldAgent.URL, Token: "old-token"}}, s.cfg.Nodes)
}

func TestService_Deregister(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestService(t, configFile)

	require.NoError(t, s.OnRegister(t.Context(), registerRequest("node1", agentSrv.URL, "agent-token")))
	require.NoError(t, s.Deregister(t.Context(), DeregisterRequest{NodeName: "node1"}))

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, node.Status(node.StatusRemoved), nodes[0].Status())
	assert.Equal(t, []*AgentNode{{Name: "node1", Addr: agentSrv.URL, Token: "agent-token"}}, readConfigNodes(t, configFile))

	err := s.Deregister(t.Context(), DeregisterRequest{NodeName: "unknown"})
	assert.ErrorIs(t, err, ErrNodeNotFound)
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

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func TestConfig_LoadFromFile_freshState(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")

	writeConfig(t, configFile, `
http:
  host: 127.0.0.1
nodes:
  - name: node1
    addr: http://node1:7430
    token: token1
  - name: node2
    addr: http://node2:7430
    token: token2
`)

	first := NewConfig()
	require.NoError(t, first.LoadFromFile(configFile))
	require.Len(t, first.Nodes, 2)
	require.Equal(t, "127.0.0.1", first.Http.Host)

	writeConfig(t, configFile, `
nodes:
  - name: node1
    addr: http://node1:7430
    token: token1
`)

	second := NewConfig()
	require.NoError(t, second.LoadFromFile(configFile))
	assert.Empty(t, second.Http.Host)
	assert.Equal(t, []*AgentNode{{Name: "node1", Addr: "http://node1:7430", Token: "token1"}}, second.Nodes)
}

func TestService_Reload_nodes(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")

	writeConfig(t, configFile, `
http:
  token: server-token
nodes:
  - name: keep
    addr: http://keep:7430
    token: keep-token
  - name: change
    addr: http://change:7430
    token: old-token
  - name: drop
    addr: http://drop:7430
    token: drop-token
  - name: removed
    addr: http://removed:7430
    token: removed-token
`)

	cfg := NewConfig()
	require.NoError(t, cfg.LoadFromFile(configFile))
	s := NewService(cfg)

	byName := func() map[string]*node.Node {
		m := make(map[string]*node.Node)
		for _, n := range s.GetNodes() {
			m[n.Name] = n
		}
		return m
	}

	before := byName()
	before["keep"].SetStatus(node.StatusReady)
	before["change"].SetStatus(node.StatusReady)
	before["change"].SetLabels(map[string]string{"region": "eu"})
	before["removed"].SetStatus(node.StatusRemoved)

	writeConfig(t, configFile, `
http:
  token: server-token
nodes:
  - name: keep
    addr: http://keep:7430
    token: keep-token
  - name: change
    addr: http://changed:7430
    token: new-token
  - name: removed
    addr: http://removed-new:7430
    token: removed-token
  - name: new
    addr: http://new:7430
    token: new-token
`)

	require.NoError(t, s.Reload())

	after := byName()
	require.Len(t, after, 5)

	assert.Same(t, before["keep"], after["keep"])
	assert.Equal(t, node.Status(node.StatusReady), after["keep"].Status())

	assert.Same(t, before["change"], after["change"])
	assert.Equal(t, "http://changed:7430", after["change"].Addr())
	assert.Equal(t, "new-token", after["change"].Token())
	assert.Equal(t, map[string]string{"region": "eu"}, after["change"].Labels())
	assert.Equal(t, node.Status(node.StatusReady), after["change"].Status())

	assert.Equal(t, node.Status(node.StatusRemoved), after["drop"].Status())

	assert.Equal(t, "http://removed-new:7430", after["removed"].Addr())
	assert.Equal(t, node.Status(node.StatusRemoved), after["removed"].Status())

	assert.Equal(t, "http://new:7430", after["new"].Addr())
	assert.Equal(t, node.Status(node.StatusNotReady), after["new"].Status())

	assert.Equal(t, []*AgentNode{
		{Name: "keep", Addr: "http://keep:7430", Token: "keep-token"},
		{Name: "change", Addr: "http://changed:7430", Token: "new-token"},
		{Name: "removed", Addr: "http://removed-new:7430", Token: "removed-token"},
		{Name: "new", Addr: "http://new:7430", Token: "new-token"},
	}, s.cfg.Nodes)
}

func TestService_Reload_invalidFile(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")

	writeConfig(t, configFile, `
nodes:
  - name: node1
    addr: http://node1:7430
    token: token1
`)

	cfg := NewConfig()
	require.NoError(t, cfg.LoadFromFile(configFile))
	s := NewService(cfg)

	want := []*AgentNode{{Name: "node1", Addr: "http://node1:7430", Token: "token1"}}

	writeConfig(t, configFile, "nodes: [unclosed\n")
	assert.Error(t, s.Reload())

	require.NoError(t, os.Remove(configFile))
	assert.Error(t, s.Reload())

	nodes := s.GetNodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, "node1", nodes[0].Name)
	assert.Equal(t, node.Status(node.StatusNotReady), nodes[0].Status())
	assert.Equal(t, want, s.cfg.Nodes)
}

func TestService_Reload_httpNotApplied(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")

	writeConfig(t, configFile, `
http:
  host: 127.0.0.1
  port: "7422"
  token: server-token
`)

	cfg := NewConfig()
	require.NoError(t, cfg.LoadFromFile(configFile))
	s := NewService(cfg)

	writeConfig(t, configFile, `
http:
  host: 0.0.0.0
  port: "8000"
  token: other-token
nodes:
  - name: node1
    addr: http://node1:7430
    token: token1
`)

	require.NoError(t, s.Reload())

	assert.Equal(t, Http{Host: "127.0.0.1", Port: "7422", Token: "server-token"}, s.cfg.Http)
	assert.Len(t, s.GetNodes(), 1)
}

func TestService_Reload_concurrent(t *testing.T) {
	agentSrv := fakeAgent(t, "agent-token")
	configFile := filepath.Join(t.TempDir(), "config.yaml")

	writeConfig(t, configFile, `
http:
  token: server-token
nodes:
  - name: node1
    addr: http://node1:7430
    token: token1
`)

	cfg := NewConfig()
	require.NoError(t, cfg.LoadFromFile(configFile))
	s := NewService(cfg)

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 20 {
			assert.NoError(t, s.Reload())
		}
	})

	wg.Go(func() {
		for range 20 {
			_ = len(s.GetNodes())
		}
	})

	wg.Go(func() {
		for range 5 {
			assert.NoError(t, s.OnRegister(t.Context(), registerRequest("node2", agentSrv.URL, "agent-token")))
		}
	})

	wg.Wait()

	names := make([]string, 0)
	for _, n := range s.GetNodes() {
		names = append(names, n.Name)
	}
	assert.ElementsMatch(t, []string{"node1", "node2"}, names)
}
