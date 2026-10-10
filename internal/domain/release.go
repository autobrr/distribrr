package domain

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"

	"github.com/autobrr/distribrr/pkg/sharedhttp"

	"github.com/autobrr/go-torrent/metainfo"
	"github.com/avast/retry-go"
	"github.com/pkg/errors"
	"golang.org/x/net/publicsuffix"
)

//var client = &http.Client{
//	Timeout: time.Second * 45,
//	Transport: sharedhttp.TransportTLSInsecure,
//}

var ErrUnrecoverableError = errors.New("unrecoverable error")

func NewRelease(url string, name string, indexer string) *Release {
	return &Release{
		Url:     url,
		Hash:    "",
		Name:    name,
		Indexer: indexer,
		Size:    0,
	}
}

type Release struct {
	Url       string
	RawCookie string
	Hash      string
	Name      string
	Indexer   string
	Size      uint64
	// File is the .torrent, set by DownloadTorrentFile.
	File []byte
}

func (r *Release) SetCookies(cookie string) {
	r.RawCookie = cookie
}

// downloadAttemptTimeout bounds one attempt to fetch the .torrent file.
const downloadAttemptTimeout = 20 * time.Second

func (r *Release) DownloadTorrentFile(ctx context.Context) error {
	if r.Url == "" {
		return errors.New("download_file: url can't be empty")
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return errors.Wrap(err, "could not create cookiejar")
	}

	client := &http.Client{
		Transport: sharedhttp.TransportTLSInsecure,
		Jar:       jar,
		Timeout:   downloadAttemptTimeout,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Url, nil)
	if err != nil {
		return errors.Wrap(err, "error downloading file")
	}

	if r.RawCookie != "" {
		// set the cookie on the header instead of req.AddCookie
		// since we have a raw cookie like "uid=10; pass=000"
		req.Header.Set("Cookie", r.RawCookie)
	}

	errFunc := retry.Do(func() error {
		// Get the data
		resp, err := client.Do(req)
		if err != nil {
			return errors.Wrap(err, "error downloading file")
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			unRecoverableErr := errors.Wrapf(ErrUnrecoverableError, "unrecoverable error downloading torrent (%v) file (%v) from '%v' - status code: %d", r.Name, r.Url, r.Indexer, resp.StatusCode)

			if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 500 {
				return retry.Unrecoverable(unRecoverableErr)
			}

			return errors.Errorf("unexpected status: %v", resp.StatusCode)
		}

		file, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "error reading torrent file")
		}

		meta, err := metainfo.Load(bytes.NewReader(file))
		if err != nil {
			return retry.Unrecoverable(errors.Wrapf(err, "metainfo could not load file contents: %v", r.Name))
		}

		torrentMetaInfo, err := meta.UnmarshalInfo()
		if err != nil {
			return retry.Unrecoverable(errors.Wrapf(err, "metainfo could not unmarshal info from torrent: %v", r.Name))
		}

		hashInfoBytes := meta.HashInfoBytes().Bytes()
		if len(hashInfoBytes) < 1 {
			return retry.Unrecoverable(errors.New("could not read infohash"))
		}

		r.Hash = meta.HashInfoBytes().String()
		r.Size = uint64(torrentMetaInfo.TotalLength())
		r.File = file

		return nil
	},
		// stop retrying once ctx is done, so the agent's task deadline bounds
		// the whole download
		retry.Context(ctx),
		retry.Delay(time.Second),
		retry.Attempts(3),
		retry.MaxJitter(500*time.Millisecond),
	)

	return errFunc
}
