package sharedhttp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewServer(t *testing.T) {
	srv := NewServer(http.NotFoundHandler())

	assert.Equal(t, readHeaderTimeout, srv.ReadHeaderTimeout)
	assert.Equal(t, readTimeout, srv.ReadTimeout)
	assert.Equal(t, idleTimeout, srv.IdleTimeout)
	// handlers wait on agents for longer than any sane write timeout
	assert.Zero(t, srv.WriteTimeout)
}
