// Package ocap talks to an OCAP2 web server: it lists recordings and opens recording data.
package ocap

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound reports that OCAP has no data file for a recording (it was deleted, or the
// recording was registered without one).
var ErrNotFound = errors.New("recording file not found in OCAP")

// maxListSize bounds the operations list response (a few hundred bytes per recording).
const maxListSize = 64 << 20

const userAgent = "ocap-statistics/1.0 (+https://github.com/smitt14ua/ocap-statistics)"

// Operation is a recording as returned by GET /api/v1/operations.
type Operation struct {
	ID               int64   `json:"id"`
	WorldName        string  `json:"world_name"`
	MissionName      string  `json:"mission_name"`
	MissionDuration  float64 `json:"mission_duration"`
	Filename         string  `json:"filename"`
	Date             string  `json:"date"`
	Tag              string  `json:"tag"`
	ConversionStatus string  `json:"conversionStatus"`
}

// StatusStreaming is the conversion status of a recording that OCAP is still receiving over
// its WebSocket ingest. OCAP lists it from the mission start, but writes its data file only
// when the mission ends.
const StatusStreaming = "streaming"

// Live reports whether OCAP is still recording the operation, so it has no data file yet.
func (o Operation) Live() bool { return o.ConversionStatus == StatusStreaming }

// Day returns the recording date as YYYY-MM-DD.
func (o Operation) Day() string {
	if len(o.Date) >= 10 {
		return o.Date[:10]
	}
	return o.Date
}

type Client struct {
	baseURL string
	dataDir string
	token   string
	http    *http.Client
	log     *slog.Logger
}

func NewClient(baseURL, dataDir, token string, timeout time.Duration, log *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		dataDir: dataDir,
		token:   token,
		http:    &http.Client{Timeout: timeout},
		log:     log,
	}
}

// ListOperations returns recordings filtered by tag (OCAP matches it as a substring) and
// dated on or after newer (YYYY-MM-DD). Empty arguments disable the filter.
func (c *Client) ListOperations(ctx context.Context, tag, newer string) ([]Operation, error) {
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if newer != "" {
		q.Set("newer", newer)
	}
	u := c.baseURL + "/api/v1/operations"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ops []Operation
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListSize)).Decode(&ops); err != nil {
		return nil, fmt.Errorf("decode operations: %w", err)
	}
	return ops, nil
}

// OpenMission returns the decompressed JSON of a recording. It reads from the local data
// directory when configured, otherwise (or when the file is not there) it downloads
// <base>/data/<filename>.json.gz.
func (c *Client) OpenMission(ctx context.Context, filename string) (io.ReadCloser, error) {
	filename = strings.TrimSuffix(filename, "/")
	if filename == "" || strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		return nil, fmt.Errorf("invalid recording filename %q", filename)
	}
	if c.dataDir != "" {
		path := filepath.Join(c.dataDir, filename+".json.gz")
		f, err := os.Open(path)
		if err == nil {
			return maybeGunzip(f)
		}
		// Not only a missing file: OCAP writes recordings received over its WebSocket
		// ingest with mode 0600, unreadable for a container running as another user.
		// OCAP itself can still serve them over HTTP.
		c.log.Warn("cannot read recording from data dir, downloading it", "path", path, "err", err)
	}
	resp, err := c.get(ctx, c.baseURL+"/data/"+url.PathEscape(filename)+".json.gz")
	if err != nil {
		return nil, err
	}
	return maybeGunzip(resp.Body)
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("GET %s: %w", u, ErrNotFound)
		}
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

// maybeGunzip wraps rc in a gzip reader when the content starts with the gzip magic bytes.
// The HTTP transport already decodes Content-Encoding: gzip, but files stored or proxied
// without that header arrive still compressed.
func maybeGunzip(rc io.ReadCloser) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(rc, 64*1024)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		rc.Close()
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			rc.Close()
			return nil, err
		}
		return readCloser{Reader: zr, close: func() error { zr.Close(); return rc.Close() }}, nil
	}
	return readCloser{Reader: br, close: rc.Close}, nil
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }
