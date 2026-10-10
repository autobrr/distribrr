// Package faketracker serves generated .torrent files over HTTP for tests.
package faketracker

import (
	"bytes"
	"crypto/sha1"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
)

// PieceLength is the piece length of generated torrents.
const PieceLength = 256 << 10

const torrentPrefix = "/torrent/"

type entry struct {
	data   []byte
	status int

	// delay holds every answer back; failFirst answers failStatus to the
	// first failFirst requests
	delay      time.Duration
	failFirst  int
	failStatus int
}

// Tracker serves .torrent files. All methods are safe for concurrent use.
type Tracker struct {
	srv *httptest.Server

	mu        sync.Mutex
	entries   map[string]entry
	downloads map[string]int
}

// New starts a fake tracker. It is closed on t.Cleanup.
func New(t testing.TB) *Tracker {
	t.Helper()

	tr := &Tracker{
		entries:   map[string]entry{},
		downloads: map[string]int{},
	}

	tr.srv = httptest.NewServer(http.HandlerFunc(tr.serveHTTP))
	t.Cleanup(tr.srv.Close)

	return tr
}

// URL is the base URL of the tracker.
func (tr *Tracker) URL() string { return tr.srv.URL }

// Add registers a valid single-file torrent called name with a total length of
// size bytes and returns its download URL and hex infohash. Piece hashes are
// derived from name, so the same name and size always give the same infohash.
// Adding an existing name replaces it.
func (tr *Tracker) Add(name string, size int64) (string, string) {
	data, hash, err := buildTorrent(name, size, tr.srv.URL+"/announce")
	if err != nil {
		// only fails on bencode errors, which a fixed struct cannot produce
		panic(err)
	}

	tr.mu.Lock()
	tr.entries[name] = entry{data: data, status: http.StatusOK}
	tr.mu.Unlock()

	return tr.urlFor(name), hash
}

// AddStatus registers name as a URL that always answers status.
func (tr *Tracker) AddStatus(name string, status int) string {
	tr.mu.Lock()
	tr.entries[name] = entry{status: status}
	tr.mu.Unlock()

	return tr.urlFor(name)
}

// SetDelay holds every answer for name back by d, like a slow tracker. A
// request whose client gives up is answered early.
func (tr *Tracker) SetDelay(name string, d time.Duration) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	e := tr.entries[name]
	e.delay = d
	tr.entries[name] = e
}

// FailFirst makes the first n requests for name answer status, like a
// tracker that is briefly overloaded. Later requests are served normally.
func (tr *Tracker) FailFirst(name string, n int, status int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	e := tr.entries[name]
	e.failFirst = n
	e.failStatus = status
	tr.entries[name] = e
}

// Torrent returns the .torrent bytes registered for name, or nil.
func (tr *Tracker) Torrent(name string) []byte {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	return bytes.Clone(tr.entries[name].data)
}

// Downloads returns how many times name was requested, whatever the answer.
func (tr *Tracker) Downloads(name string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	return tr.downloads[name]
}

func (tr *Tracker) urlFor(name string) string {
	return tr.srv.URL + torrentPrefix + url.PathEscape(name) + ".torrent"
}

func (tr *Tracker) serveHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, torrentPrefix)
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSuffix(rest, ".torrent")

	tr.mu.Lock()
	tr.downloads[name]++
	e, ok := tr.entries[name]
	failing := ok && e.failFirst > 0
	if failing {
		e.failFirst--
		tr.entries[name] = e
	}
	tr.mu.Unlock()

	if ok && e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-r.Context().Done():
			return
		}
	}

	switch {
	case !ok:
		http.NotFound(w, r)
	case failing:
		http.Error(w, http.StatusText(e.failStatus), e.failStatus)
	case e.status != http.StatusOK:
		http.Error(w, http.StatusText(e.status), e.status)
	default:
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(e.data)
	}
}

func buildTorrent(name string, size int64, announce string) ([]byte, string, error) {
	numPieces := (size + PieceLength - 1) / PieceLength

	seed := sha1.Sum([]byte(name))
	pieces := make([]byte, 0, numPieces*sha1.Size)
	for i := range numPieces {
		h := sha1.Sum(append(seed[:], byte(i), byte(i>>8), byte(i>>16), byte(i>>24)))
		pieces = append(pieces, h[:]...)
	}

	info := metainfo.Info{
		PieceLength: PieceLength,
		Pieces:      pieces,
		Name:        name,
		Length:      size,
	}

	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		return nil, "", errors.Wrap(err, "could not marshal info")
	}

	mi := metainfo.MetaInfo{
		InfoBytes: infoBytes,
		Announce:  announce,
	}

	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, "", errors.Wrap(err, "could not write metainfo")
	}

	return buf.Bytes(), mi.HashInfoBytes().HexString(), nil
}
