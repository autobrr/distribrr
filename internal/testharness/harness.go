// Package testharness runs a distribrr server and agents in-process for
// integration tests, with fake qBittorrent clients behind the agents.
//
// The server and every agent listen on their own 127.0.0.1 port and talk over
// real HTTP, exactly as in production. Config files live in t.TempDir(), so
// registration persistence and reloads go through the same code paths too.
package testharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/pkg/agent"
	"github.com/autobrr/distribrr/pkg/server"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	// Token is the server API token, used by autobrr and agents alike.
	Token = "server-token"

	// WaitTimeout bounds every Wait* helper.
	WaitTimeout = 5 * time.Second

	pollInterval = 10 * time.Millisecond
	stopTimeout  = 5 * time.Second
)

// Timings for the in-process services. RegisterInterval is also the timeout of
// one join attempt, so it must leave room for the server's token callback
// under -race.
const (
	healthCheckInterval = 50 * time.Millisecond
	registerInterval    = 250 * time.Millisecond
	dirUsageInterval    = 50 * time.Millisecond
)

// Harness is a running server plus the agents added to it.
type Harness struct {
	t testing.TB

	url        string
	addr       string
	configFile string
	reload     chan struct{}
	http       *http.Client
	opts       []Option

	m    sync.Mutex
	stop func() error
}

// Option configures a Harness.
type Option func(*server.Service)

// WithHealthCheckInterval overrides how often the server health checks agents.
func WithHealthCheckInterval(d time.Duration) Option {
	return func(s *server.Service) {
		s.HealthCheckInterval = d
	}
}

// WithTaskTimeout overrides how long the server waits for one agent to accept
// a task.
func WithTaskTimeout(d time.Duration) Option {
	return func(s *server.Service) {
		s.TaskTimeout = d
	}
}

// New starts a server on a random local port with an empty config file. It is
// stopped on t.Cleanup, after every agent added later.
func New(t testing.TB, opts ...Option) *Harness {
	t.Helper()

	h := &Harness{
		t:          t,
		addr:       "127.0.0.1:0",
		configFile: filepath.Join(t.TempDir(), "config_server.yaml"),
		reload:     make(chan struct{}),
		http:       &http.Client{Timeout: 30 * time.Second},
		opts:       opts,
	}

	writeYAML(t, h.configFile, map[string]any{
		"http": map[string]any{"host": "127.0.0.1", "port": "0", "token": Token},
	})

	h.start(t)
	t.Cleanup(func() {
		if err := h.stopServer(); err != nil {
			t.Errorf("server: %v", err)
		}
	})

	return h
}

// start runs a server on h.addr from the config file. The first start picks a
// random port and keeps it, so a restarted server has the same URL.
func (h *Harness) start(t testing.TB) {
	t.Helper()

	cfg := server.NewConfig()
	require.NoError(t, cfg.LoadFromFile(h.configFile))

	svc := server.NewService(cfg)
	svc.HealthCheckInterval = healthCheckInterval
	for _, o := range h.opts {
		o(svc)
	}

	ln, err := net.Listen("tcp", h.addr)
	require.NoError(t, err)
	h.addr = ln.Addr().String()
	h.url = "http://" + h.addr

	h.m.Lock()
	h.stop = serve(func(ctx context.Context) error { return svc.Serve(ctx, ln, h.reload) })
	h.m.Unlock()
}

func (h *Harness) stopServer() error {
	h.m.Lock()
	stop := h.stop
	h.stop = nil
	h.m.Unlock()

	if stop == nil {
		return nil
	}

	return stop()
}

// RestartServer stops the server and starts a new one on the same address
// from the config file, like a process restart: nodes come back from the
// config, everything else is lost.
func (h *Harness) RestartServer(t testing.TB) {
	t.Helper()

	require.NoError(t, h.stopServer())
	h.start(t)
}

// URL is the base URL of the server.
func (h *Harness) URL() string { return h.url }

// ConfigFile is the server config file. Registrations are persisted to it.
func (h *Harness) ConfigFile() string { return h.configFile }

// Webhook posts tk to the task endpoint like autobrr does, with the right
// token, and returns the status code and body.
func (h *Harness) Webhook(t testing.TB, tk task.Task) (int, string) {
	t.Helper()

	return h.WebhookWithToken(t, Token, tk)
}

// WebhookWithToken is Webhook with token as ?apikey=. An empty token sends none.
func (h *Harness) WebhookWithToken(t testing.TB, token string, tk task.Task) (int, string) {
	t.Helper()

	body, err := json.Marshal(tk)
	require.NoError(t, err)

	u := h.url + "/api/v1/tasks"
	if token != "" {
		u += "?apikey=" + url.QueryEscape(token)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	return h.Do(t, req)
}

// Do sends req to the server and returns the status code and body.
func (h *Harness) Do(t testing.TB, req *http.Request) (int, string) {
	t.Helper()

	resp, err := h.http.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(b)
}

// NodeInfo is a node as the server lists it.
type NodeInfo struct {
	Name   string            `json:"name"`
	Addr   string            `json:"addr"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
}

// Nodes lists the nodes the server knows, removed ones included.
func (h *Harness) Nodes(t testing.TB) []NodeInfo {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.url+"/api/v1/node/", nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Token", Token)

	status, body := h.Do(t, req)
	require.Equal(t, http.StatusOK, status, body)

	var nodes []NodeInfo
	require.NoError(t, json.Unmarshal([]byte(body), &nodes))

	return nodes
}

// Node returns the node called name, or false.
func (h *Harness) Node(t testing.TB, name string) (NodeInfo, bool) {
	t.Helper()

	for _, n := range h.Nodes(t) {
		if n.Name == name {
			return n, true
		}
	}

	return NodeInfo{}, false
}

// WaitNode waits until the server reports node name with status (READY,
// UNKNOWN, REMOVED, ...) and returns it.
func (h *Harness) WaitNode(t testing.TB, name string, status string, timeout time.Duration) NodeInfo {
	t.Helper()

	return h.waitNode(t, name, timeout, func(n NodeInfo) bool { return n.Status == status })
}

// WaitNodeMatch waits until node name exists and cond holds for it.
func (h *Harness) WaitNodeMatch(t testing.TB, name string, cond func(NodeInfo) bool) NodeInfo {
	t.Helper()

	return h.waitNode(t, name, WaitTimeout, cond)
}

func (h *Harness) waitNode(t testing.TB, name string, timeout time.Duration, cond func(NodeInfo) bool) NodeInfo {
	t.Helper()

	var last NodeInfo
	waitFor(t, timeout, func() bool {
		n, ok := h.Node(t, name)
		last = n
		return ok && cond(n)
	}, func() string {
		return fmt.Sprintf("node %s never matched, last seen: %+v", name, last)
	})

	return last
}

// ReloadServer rewrites the server config file through mutate and makes the
// server reload it. It returns once the server picked up the reload signal;
// wait for its effect with WaitNode. Do not call it while an agent registers,
// since registration writes the same file.
func (h *Harness) ReloadServer(t testing.TB, mutate func(*server.Config)) {
	t.Helper()

	data, err := os.ReadFile(h.configFile)
	require.NoError(t, err)

	var cfg server.Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))

	mutate(&cfg)

	out, err := yaml.Marshal(&cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.configFile, out, 0o600))

	select {
	case h.reload <- struct{}{}:
	case <-time.After(WaitTimeout):
		t.Fatal("server did not pick up the reload")
	}
}

// WaitReady waits until every named agent is READY on the server and all of
// its clients report READY in their stats (so storage has been measured too).
func (h *Harness) WaitReady(t testing.TB, agents ...*Agent) {
	t.Helper()

	for _, a := range agents {
		h.WaitNode(t, a.Name(), "READY", WaitTimeout)
		a.WaitClients(t, func(cs stats.ClientStats) bool {
			return cs.Status == stats.ClientStatusReady
		})
	}
}

// AgentSpec describes an agent. Labels and clients can be changed with
// Agent.Reload.
type AgentSpec struct {
	Name    string
	Labels  map[string]string
	Clients []ClientSpec
	// Unregistered keeps the agent's link to the server cut from the start,
	// so it never registers; add it to the server config with ReloadServer.
	Unregistered bool
	// TaskTimeout overrides the agent's agent.Service.TaskTimeout when set.
	TaskTimeout time.Duration
}

// ClientSpec describes one qBittorrent client of an agent.
type ClientSpec struct {
	Name string
	Qbit *fakeqbit.Server
	// MaxActiveDownloads of 0 means unlimited.
	MaxActiveDownloads int
	// Storage rule paths must exist; use t.TempDir().
	Storage []agent.StorageRule
}

// Agent is an agent started by AddAgent.
type Agent struct {
	h     *Harness
	t     testing.TB
	spec  AgentSpec
	token string

	configFile string
	// link is the agent's only way to the server: a proxy that Kill cuts so
	// the agent cannot deregister.
	link    *httptest.Server
	linkCut atomic.Bool

	url    string
	reload chan struct{}
	stop   func() error
}

// AddAgent starts an agent for spec that registers with the server, and
// returns once it is serving. Use WaitReady to wait for registration. It is
// stopped on t.Cleanup.
func (h *Harness) AddAgent(t testing.TB, spec AgentSpec) *Agent {
	t.Helper()

	require.NotEmpty(t, spec.Name, "agent needs a name")

	target, err := url.Parse(h.url)
	require.NoError(t, err)

	a := &Agent{
		h:          h,
		t:          t,
		spec:       spec,
		token:      spec.Name + "-token",
		configFile: filepath.Join(t.TempDir(), "config_agent.yaml"),
	}
	a.linkCut.Store(spec.Unregistered)

	proxy := httputil.NewSingleHostReverseProxy(target)
	a.link = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.linkCut.Load() {
			http.Error(w, "link cut", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(a.link.Close)

	a.start()

	t.Cleanup(func() {
		if a.stop != nil {
			if err := a.stop(); err != nil {
				t.Errorf("agent %s: %v", a.spec.Name, err)
			}
		}
	})

	return a
}

// Name is the node name of the agent.
func (a *Agent) Name() string { return a.spec.Name }

// URL is the agent's current base URL; it changes on Restart.
func (a *Agent) URL() string { return a.url }

// Token is the agent's API token.
func (a *Agent) Token() string { return a.token }

// start writes the config file and runs the agent on a new port.
func (a *Agent) start() {
	t := a.t
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a.url = "http://" + ln.Addr().String()

	a.writeConfig()

	cfg := agent.NewConfig()
	require.NoError(t, cfg.LoadFromFile(a.configFile))
	require.NoError(t, cfg.Validate())

	svc := agent.NewService(cfg)
	svc.RegisterInterval = registerInterval
	svc.DirUsageInterval = dirUsageInterval
	svc.ReannounceInterval = 1
	svc.ReannounceMaxAttempts = 2
	if a.spec.TaskTimeout > 0 {
		svc.TaskTimeout = a.spec.TaskTimeout
	}

	a.reload = make(chan struct{})
	reload := a.reload
	a.stop = serve(func(ctx context.Context) error { return svc.Serve(ctx, ln, reload) })
}

// writeConfig renders the spec as the agent's YAML config. Keys follow the
// struct field names, which is what koanf matches on.
func (a *Agent) writeConfig() {
	clients := map[string]any{}
	for _, c := range a.spec.Clients {
		require.NotNil(a.t, c.Qbit, "client %s needs a fake qbit", c.Name)

		storage := make([]map[string]any, 0, len(c.Storage))
		for _, s := range c.Storage {
			storage = append(storage, map[string]any{"path": s.Path, "minFree": s.MinFree, "maxUsage": s.MaxUsage})
		}

		clients[c.Name] = map[string]any{
			"host": c.Qbit.URL(),
			"user": c.Qbit.Username(),
			"pass": c.Qbit.Password(),
			"rules": map[string]any{
				"torrents": map[string]any{"maxActiveDownloads": c.MaxActiveDownloads},
				"storage":  storage,
			},
		}
	}

	labels := a.spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	writeYAML(a.t, a.configFile, map[string]any{
		"http":    map[string]any{"host": "127.0.0.1", "port": "0", "token": a.token},
		"agent":   map[string]any{"nodeName": a.spec.Name, "clientAddr": a.url, "labels": labels},
		"manager": map[string]any{"addr": a.link.URL, "token": Token},
		"clients": clients,
	})
}

// Stop shuts the agent down gracefully: it deregisters, so the server marks
// the node REMOVED.
func (a *Agent) Stop() {
	a.t.Helper()

	require.NoError(a.t, a.halt())
}

// Kill simulates a crash: the agent stops serving without deregistering. The
// agent's Serve always deregisters on shutdown, so Kill first cuts the link
// to the server; the deregister fails at once and the server only notices
// through failing health checks (node UNKNOWN). Restart restores the link.
func (a *Agent) Kill() {
	a.t.Helper()

	a.linkCut.Store(true)

	// the failed deregister is the point, so its error is expected
	_ = a.halt()
}

func (a *Agent) halt() error {
	stop := a.stop
	a.stop = nil

	if stop == nil {
		return errors.Errorf("agent %s is not running", a.spec.Name)
	}

	return stop()
}

// Restart starts the agent again on a new port after Stop or Kill (it stops a
// running agent gracefully first). It registers again with the new address.
func (a *Agent) Restart() {
	a.t.Helper()

	if a.stop != nil {
		a.Stop()
	}

	a.linkCut.Store(a.spec.Unregistered)
	a.start()
}

// Reload changes the spec through mutate, rewrites the config file and makes
// the agent reload it. It returns once the agent picked up the reload signal.
func (a *Agent) Reload(mutate func(*AgentSpec)) {
	a.t.Helper()

	mutate(&a.spec)
	a.writeConfig()

	select {
	case a.reload <- struct{}{}:
	case <-time.After(WaitTimeout):
		a.t.Fatalf("agent %s did not pick up the reload", a.spec.Name)
	}
}

// Stats fetches the agent's stats as the server would.
func (a *Agent) Stats(t testing.TB) (*stats.Stats, error) {
	t.Helper()

	return agent.NewClient(a.url, a.spec.Name, a.token).GetStats(t.Context())
}

// WaitClients waits until cond holds for every client in the agent's stats.
func (a *Agent) WaitClients(t testing.TB, cond func(stats.ClientStats) bool) {
	t.Helper()

	var (
		last    map[string]stats.ClientStats
		lastErr error
	)
	waitFor(t, WaitTimeout, func() bool {
		st, err := a.Stats(t)
		lastErr = err
		if err != nil {
			return false
		}

		last = st.ClientStats
		for _, cs := range st.ClientStats {
			if !cond(cs) {
				return false
			}
		}

		return true
	}, func() string {
		return fmt.Sprintf("agent %s clients never matched, last seen: %+v (err: %v)", a.spec.Name, last, lastErr)
	})
}

// waitFor polls cond on the calling goroutine, so cond may use require, and
// fails the test with msg() when it does not hold within timeout.
func waitFor(t testing.TB, timeout time.Duration, cond func() bool, msg func() string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg())
		}

		time.Sleep(pollInterval)
	}
}

// serve runs fn in the background and returns a func that cancels its ctx and
// waits for it to return.
func serve(fn func(ctx context.Context) error) func() error {
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- fn(ctx)
	}()

	return sync.OnceValue(func() error {
		cancel()

		select {
		case err := <-errCh:
			return err
		case <-time.After(stopTimeout):
			return errors.New("did not stop in time")
		}
	})
}

func writeYAML(t testing.TB, path string, v any) {
	t.Helper()

	data, err := yaml.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}
