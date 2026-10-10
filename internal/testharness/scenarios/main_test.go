// Package scenarios runs end-to-end scenarios against an in-process server,
// agents and fake qBittorrent clients built by package testharness.
//
// Logs are off; set DISTRIBRR_TEST_LOG=1 to see them.
package scenarios

import (
	"os"
	"testing"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/internal/testharness/faketracker"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/rs/zerolog"
)

func TestMain(m *testing.M) {
	if os.Getenv("DISTRIBRR_TEST_LOG") == "" {
		zerolog.SetGlobalLevel(zerolog.Disabled)
	}

	os.Exit(m.Run())
}

const (
	kb = 1000
	mb = 1000 * kb
)

// env is a harness with a tracker, the setup most scenarios share.
type env struct {
	*testharness.Harness
	tracker *faketracker.Tracker
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Parallel()

	return &env{
		Harness: testharness.New(t),
		tracker: faketracker.New(t),
	}
}

// agent adds an agent with one client named "qbit" in front of a new fake
// qbit, and returns both.
func (e *env) agent(t *testing.T, name string, labels map[string]string, opts ...func(*testharness.ClientSpec)) (*testharness.Agent, *fakeqbit.Server) {
	t.Helper()

	qbit := fakeqbit.New(t)

	client := testharness.ClientSpec{Name: "qbit", Qbit: qbit}
	for _, o := range opts {
		o(&client)
	}

	a := e.AddAgent(t, testharness.AgentSpec{
		Name:    name,
		Labels:  labels,
		Clients: []testharness.ClientSpec{client},
	})

	return a, qbit
}

// release registers a torrent of size bytes on the tracker and returns a task
// for it, without size like autobrr when it does not know it.
func (e *env) release(name string, size int64) task.Task {
	u, _ := e.tracker.Add(name, size)

	return taskFor(u, name)
}

// totalAdded sums the torrents added across qbits.
func totalAdded(qbits ...*fakeqbit.Server) int {
	n := 0
	for _, q := range qbits {
		n += len(q.Added())
	}

	return n
}

func taskFor(downloadURL, name string) task.Task {
	return task.Task{DownloadURL: downloadURL, Name: name, Indexer: "fake"}
}
