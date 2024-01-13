package domain

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/avast/retry-go"
	"github.com/pkg/errors"
	"golang.org/x/net/publicsuffix"
)

var ErrUnrecoverableError = errors.New("unrecoverable error")

type Release struct {
	TorrentURL     string
	TorrentTmpFile string
	RawCookie      string
	TorrentHash    string
	TorrentName    string
	Indexer        string
	Size           uint64
}

func (r *Release) DownloadTorrentFile(ctx context.Context) error {
	//if r.IsMagnetLink(r.MagnetURI) {
	//	return fmt.Errorf("error trying to download magnet link: %s", r.MagnetURI)
	//}

	if r.TorrentURL == "" {
		return errors.New("download_file: url can't be empty")
	} else if r.TorrentTmpFile != "" {
		// already downloaded
		return nil
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return errors.Wrap(err, "could not create cookiejar")
	}

	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client := &http.Client{
		Transport: customTransport,
		Jar:       jar,
		Timeout:   time.Second * 45,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.TorrentURL, nil)
	if err != nil {
		return errors.Wrap(err, "error downloading file")
	}

	if r.RawCookie != "" {
		// set the cookie on the header instead of req.AddCookie
		// since we have a raw cookie like "uid=10; pass=000"
		req.Header.Set("Cookie", r.RawCookie)
	}

	// Create tmp file
	tmpFile, err := os.CreateTemp("", "distribrr-")
	if err != nil {
		return errors.Wrap(err, "error creating tmp file")
	}
	defer tmpFile.Close()

	errFunc := retry.Do(func() error {
		// Get the data
		resp, err := client.Do(req)
		if err != nil {
			return errors.Wrap(err, "error downloading file")
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			unRecoverableErr := errors.Wrapf(ErrUnrecoverableError, "unrecoverable error downloading torrent (%v) file (%v) from '%v' - status code: %d", r.TorrentName, r.TorrentURL, r.Indexer, resp.StatusCode)

			if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 405 {
				return retry.Unrecoverable(unRecoverableErr)
			}

			return errors.Errorf("unexpected status: %v", resp.StatusCode)
		}

		resetTmpFile := func() {
			tmpFile.Seek(0, io.SeekStart)
			tmpFile.Truncate(0)
		}

		// Write the body to file
		if _, err := io.Copy(tmpFile, resp.Body); err != nil {
			resetTmpFile()
			return errors.Wrapf(err, "error writing downloaded file: %v", tmpFile.Name())
		}

		meta, err := metainfo.LoadFromFile(tmpFile.Name())
		if err != nil {
			resetTmpFile()
			return errors.Wrapf(err, "metainfo could not load file contents: %v", tmpFile.Name())
		}

		torrentMetaInfo, err := meta.UnmarshalInfo()
		if err != nil {
			resetTmpFile()
			return errors.Wrapf(err, "metainfo could not unmarshal info from torrent: %v", tmpFile.Name())
		}

		hashInfoBytes := meta.HashInfoBytes().Bytes()
		if len(hashInfoBytes) < 1 {
			resetTmpFile()
			return errors.New("could not read infohash")
		}

		r.TorrentTmpFile = tmpFile.Name()
		r.TorrentHash = meta.HashInfoBytes().String()
		r.Size = uint64(torrentMetaInfo.TotalLength())

		return nil
	},
		retry.Delay(time.Second*3),
		retry.Attempts(3),
		retry.MaxJitter(time.Second*1),
	)

	return errFunc
}
