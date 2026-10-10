// Package fakeqbit is an in-process fake of the qBittorrent WebUI API v2,
// covering the calls the agent makes through github.com/autobrr/go-qbittorrent.
package fakeqbit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autobrr/go-torrent/metainfo"
	"github.com/pkg/errors"
)

const (
	// DefaultUsername is the WebUI username the fake accepts unless WithCredentials is used.
	DefaultUsername = "admin"
	// DefaultPassword is the WebUI password the fake accepts unless WithCredentials is used.
	DefaultPassword = "adminadmin"

	// AppVersion is what app/version returns.
	AppVersion = "v5.1.2"
	// WebAPIVersion is what app/webapiVersion returns.
	WebAPIVersion = "2.11.4"

	sessionCookie = "SID"
	apiPrefix     = "/api/v2/"
)

// Torrent is a torrent as torrents/info reports it.
type Torrent struct {
	Hash       string
	Name       string
	Size       int64
	AmountLeft int64
	Progress   float64
	ETA        int64
	// State is the qBittorrent state; empty means "downloading".
	State string
}

// AddedTorrent is one URL or uploaded .torrent file accepted by torrents/add.
type AddedTorrent struct {
	// URL is empty for an uploaded file.
	URL      string
	Category string
	Tags     string
	// Options holds every form field of the request except urls.
	Options map[string]string
	// Hash, Name and Size are set for an uploaded file, and for a URL only
	// when WithAddedBecomeActive fetched the torrent.
	Hash string
	Name string
	Size int64
	At   time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithCredentials sets the WebUI username and password the fake accepts.
func WithCredentials(username, password string) Option {
	return func(s *Server) {
		s.username = username
		s.password = password
	}
}

// WithAddedBecomeActive makes every added URL become an active download: the
// fake fetches the .torrent (or parses the uploaded file) and adds a "downloading" torrent with
// zero progress. Without it torrents/add only records the request.
func WithAddedBecomeActive() Option {
	return func(s *Server) {
		s.addedBecomeActive = true
	}
}

// Server is a fake qBittorrent WebUI. All methods are safe for concurrent use.
type Server struct {
	srv *httptest.Server

	username          string
	password          string
	addedBecomeActive bool

	mu             sync.Mutex
	sessions       map[string]struct{}
	torrents       []Torrent
	preallocateAll bool
	down           bool
	failAdd        bool
	trackersOK     bool
	added          []AddedTorrent
	requests       map[string]int
}

// New starts a fake qBittorrent server. It is closed on t.Cleanup.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()

	s := &Server{
		username:   DefaultUsername,
		password:   DefaultPassword,
		sessions:   map[string]struct{}{},
		trackersOK: true,
		requests:   map[string]int{},
	}

	for _, o := range opts {
		o(s)
	}

	s.srv = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(s.srv.Close)

	return s
}

// URL is the base URL to use as the qbit client Host.
func (s *Server) URL() string { return s.srv.URL }

// Username is the WebUI username the fake accepts.
func (s *Server) Username() string { return s.username }

// Password is the WebUI password the fake accepts.
func (s *Server) Password() string { return s.password }

// Close stops the server. Further requests fail with connection refused.
func (s *Server) Close() { s.srv.Close() }

// SetActiveDownloads replaces the torrent list returned by torrents/info.
func (s *Server) SetActiveDownloads(ts ...Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.torrents = slices.Clone(ts)
}

// SetPreallocateAll sets preallocate_all in app/preferences.
func (s *Server) SetPreallocateAll(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.preallocateAll = v
}

// SetDown makes every endpoint, login included, answer 503. go-qbittorrent
// treats any 5xx as unrecoverable, so calls fail at once without retries and
// the agent reports the client as client_unreachable.
func (s *Server) SetDown(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.down = v
}

// FailAdd makes torrents/add answer 415 "Fails.", which go-qbittorrent returns
// as ErrTorrentAddFailed. Failed adds are not recorded in Added.
func (s *Server) FailAdd(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failAdd = v
}

// SetTrackersWorking sets whether torrents/trackers reports a working tracker
// (the default). When false, reannounce keeps retrying.
func (s *Server) SetTrackersWorking(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.trackersOK = v
}

// ExpireSessions drops every session, so the next request gets 403 and the
// client has to log in again.
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	clear(s.sessions)
}

// Added returns the accepted torrents/add URLs in order.
func (s *Server) Added() []AddedTorrent {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.added)
}

// WaitAdded waits until at least n URLs were added and returns them. It fails
// the test on timeout.
func (s *Server) WaitAdded(t testing.TB, n int, timeout time.Duration) []AddedTorrent {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		added := s.Added()
		if len(added) >= n {
			return added
		}

		if time.Now().After(deadline) {
			t.Fatalf("fakeqbit: waited %s for %d added torrents, got %d", timeout, n, len(added))
			return nil
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// Requests returns how many requests hit path, relative to /api/v2/
// (e.g. "torrents/add"), including rejected ones.
func (s *Server) Requests(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.requests[strings.TrimPrefix(path, apiPrefix)]
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := strings.CutPrefix(r.URL.Path, apiPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	s.requests[endpoint]++
	down := s.down
	s.mu.Unlock()

	if down {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	if endpoint == "auth/login" {
		s.login(w, r)
		return
	}

	if !s.authenticated(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch endpoint {
	case "app/version":
		writeText(w, AppVersion)
	case "app/webapiVersion":
		writeText(w, WebAPIVersion)
	case "app/preferences":
		s.preferences(w)
	case "torrents/info":
		s.torrentsInfo(w, r)
	case "torrents/add":
		s.add(w, r)
	case "torrents/trackers":
		s.trackers(w, r)
	case "torrents/reannounce":
		if !requirePost(w, r) {
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// qBittorrent answers bad credentials with 200 "Fails."
	if r.PostForm.Get("username") != s.username || r.PostForm.Get("password") != s.password {
		writeText(w, "Fails.")
		return
	}

	sid := newSID()

	s.mu.Lock()
	s.sessions[sid] = struct{}{}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeText(w, "Ok.")
}

// authenticated checks the SID cookie. Like qBittorrent, the last SID wins:
// go-qbittorrent retries a 403 with the same request after logging in again,
// and net/http appends the new jar cookie to it, so the retry sends both.
func (s *Server) authenticated(r *http.Request) bool {
	sid := ""
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			sid = c.Value
		}
	}

	if sid == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.sessions[sid]
	return ok
}

func (s *Server) preferences(w http.ResponseWriter) {
	s.mu.Lock()
	prealloc := s.preallocateAll
	s.mu.Unlock()

	writeJSON(w, map[string]any{
		"preallocate_all": prealloc,
		"save_path":       "/downloads",
	})
}

type torrentJSON struct {
	Hash       string  `json:"hash"`
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	TotalSize  int64   `json:"total_size"`
	AmountLeft int64   `json:"amount_left"`
	Progress   float64 `json:"progress"`
	ETA        int64   `json:"eta"`
	State      string  `json:"state"`
}

// downloadingStates are the states qBittorrent's "downloading" filter matches.
var downloadingStates = []string{"downloading", "stalledDL", "metaDL", "forcedMetaDL", "forcedDL", "queuedDL", "pausedDL", "stoppedDL", "checkingDL"}

func (s *Server) torrentsInfo(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")

	s.mu.Lock()
	torrents := slices.Clone(s.torrents)
	s.mu.Unlock()

	res := make([]torrentJSON, 0, len(torrents))
	for _, t := range torrents {
		state := t.State
		if state == "" {
			state = "downloading"
		}

		if filter == "downloading" && !slices.Contains(downloadingStates, state) {
			continue
		}

		res = append(res, torrentJSON{
			Hash:       t.Hash,
			Name:       t.Name,
			Size:       t.Size,
			TotalSize:  t.Size,
			AmountLeft: t.AmountLeft,
			Progress:   t.Progress,
			ETA:        t.ETA,
			State:      state,
		})
	}

	writeJSON(w, res)
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		err = r.ParseMultipartForm(1 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	failAdd := s.failAdd
	s.mu.Unlock()

	if failAdd {
		writeFails(w)
		return
	}

	options := map[string]string{}
	for k, v := range r.PostForm {
		if k != "urls" && len(v) > 0 {
			options[k] = v[0]
		}
	}

	var urls []string
	for line := range strings.SplitSeq(r.PostForm.Get("urls"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			urls = append(urls, line)
		}
	}

	// qBittorrent rejects the whole request when an uploaded file is not a
	// valid torrent
	var files []AddedTorrent
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["torrents"] {
			hash, name, size, err := readUpload(fh.Open)
			if err != nil {
				writeFails(w)
				return
			}

			files = append(files, AddedTorrent{Hash: hash, Name: name, Size: size})
		}
	}

	if len(urls) == 0 && len(files) == 0 {
		writeFails(w)
		return
	}

	added := files
	for _, u := range urls {
		at := AddedTorrent{URL: u}

		if s.addedBecomeActive {
			if hash, name, size, err := fetchTorrent(r, u); err == nil {
				at.Hash, at.Name, at.Size = hash, name, size
			}
		}

		added = append(added, at)
	}

	s.mu.Lock()
	for _, at := range added {
		at.Category = options["category"]
		at.Tags = options["tags"]
		at.Options = options
		at.At = time.Now()

		if s.addedBecomeActive && at.Hash != "" {
			s.torrents = append(s.torrents, Torrent{Hash: at.Hash, Name: at.Name, Size: at.Size, AmountLeft: at.Size, ETA: 8640000, State: "downloading"})
		}

		s.added = append(s.added, at)
	}
	s.mu.Unlock()

	writeText(w, "Ok.")
}

func writeFails(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(http.StatusUnsupportedMediaType)
	_, _ = w.Write([]byte("Fails."))
}

// readUpload parses an uploaded .torrent file.
func readUpload(open func() (multipart.File, error)) (hash, name string, size int64, err error) {
	f, err := open()
	if err != nil {
		return "", "", 0, errors.Wrap(err, "could not open upload")
	}
	defer f.Close()

	return parseTorrent(f)
}

func (s *Server) trackers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ok := s.trackersOK
	s.mu.Unlock()

	status, msg := 2, "" // TrackerStatusOK
	if !ok {
		status, msg = 4, "connection timed out" // TrackerStatusNotWorking
	}

	writeJSON(w, []map[string]any{
		{"url": "** [DHT] **", "status": 0, "tier": -1, "num_peers": 0, "num_seeds": 0, "num_leeches": 0, "num_downloaded": 0, "msg": ""},
		{"url": "http://tracker.test/announce?hash=" + r.URL.Query().Get("hash"), "status": status, "tier": 0, "num_peers": 1, "num_seeds": 1, "num_leeches": 0, "num_downloaded": 0, "msg": msg},
	})
}

// fetchTorrent downloads and parses the .torrent at u like qBittorrent would.
func fetchTorrent(r *http.Request, u string) (hash, name string, size int64, err error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		return "", "", 0, errors.Wrap(err, "could not build request")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", 0, errors.Wrap(err, "could not fetch torrent")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", 0, errors.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return parseTorrent(resp.Body)
}

// parseTorrent returns the infohash, name and total size of a .torrent.
func parseTorrent(r io.Reader) (hash, name string, size int64, err error) {
	mi, err := metainfo.Load(r)
	if err != nil {
		return "", "", 0, errors.Wrap(err, "could not load metainfo")
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		return "", "", 0, errors.Wrap(err, "could not unmarshal info")
	}

	return mi.HashInfoBytes().HexString(), info.Name, info.TotalLength(), nil
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return false
	}

	return true
}

func writeText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	_, _ = w.Write([]byte(body))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newSID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
