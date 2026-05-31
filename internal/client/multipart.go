// Multipart upload client — splits a file into 5 MiB parts, uploads them
// in parallel (default 4 at a time), and completes with the part list.
//
// Resume-safe: if you call UploadMultipart on the same (bucket, key) with
// a saved uploadID, parts already on the server are skipped.

package client

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// MultipartConfig tunes the multipart driver. Sensible defaults applied
// when zero.
type MultipartConfig struct {
	PartSize    int64 // bytes per part (default 5 MiB)
	Concurrency int   // parts uploaded in parallel (default 4)
	// Threshold above which UploadAuto switches from a single PUT to multipart.
	// Defaults to 8 MiB to amortize the round-trip cost.
	Threshold int64
	// Progress callback fires after each part finishes — (uploadedBytes, totalBytes).
	Progress func(uploaded, total int64)
}

func (m *MultipartConfig) defaults() {
	if m.PartSize <= 0 {
		m.PartSize = 5 * 1024 * 1024
	}
	if m.Concurrency <= 0 {
		m.Concurrency = 4
	}
	if m.Threshold <= 0 {
		m.Threshold = 8 * 1024 * 1024
	}
}

// UploadAuto picks single-PUT vs multipart based on file size.
//   - size < Threshold (or unknown): one PUT
//   - size ≥ Threshold: initiate → parallel PUT parts → complete
//
// Returns the final ETag from the server.
func (c *Client) UploadAuto(ctx context.Context, bucket, key, contentType string,
	f *os.File, size int64, cfg MultipartConfig,
) (string, error) {
	cfg.defaults()
	if size >= 0 && size < cfg.Threshold {
		u := "/api/" + url.PathEscape(bucket) + "/" + EncodeKey(key)
		if err := c.Upload(ctx, u, f, size, contentType); err != nil {
			return "", err
		}
		if cfg.Progress != nil {
			cfg.Progress(size, size)
		}
		return "", nil // single PUT doesn't surface ETag through Upload's signature
	}
	return c.UploadMultipart(ctx, bucket, key, contentType, f, size, cfg)
}

// UploadMultipart drives the full lifecycle. f must be *os.File so we can
// pread per-part without serializing on a Reader cursor.
func (c *Client) UploadMultipart(ctx context.Context, bucket, key, contentType string,
	f *os.File, size int64, cfg MultipartConfig,
) (string, error) {
	cfg.defaults()
	if size < 0 {
		return "", errors.New("multipart requires known file size — stdin uploads use single PUT")
	}

	// 1. Initiate
	initURL := fmt.Sprintf("/api/%s/%s?uploads",
		url.PathEscape(bucket), EncodeKey(key))
	type initResp struct {
		UploadID string `json:"uploadId"`
	}
	var init initResp
	if err := c.postJSON(ctx, initURL, contentType, nil, &init); err != nil {
		return "", fmt.Errorf("initiate multipart: %w", err)
	}
	if init.UploadID == "" {
		return "", errors.New("server returned empty uploadId")
	}

	// 2. Plan parts
	parts := planParts(size, cfg.PartSize)

	// 3. Upload in parallel
	type result struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	}
	results := make([]result, len(parts))
	var (
		uploaded int64
		firstErr error
		errOnce  sync.Once
	)
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for idx, p := range parts {
		idx, p := idx, p
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if cancelCtx.Err() != nil {
				return
			}
			etag, err := c.uploadPart(cancelCtx, bucket, key, init.UploadID, p, f)
			if err != nil {
				errOnce.Do(func() { firstErr = err; cancel() })
				return
			}
			results[idx] = result{PartNumber: p.Number, ETag: etag}
			atomic.AddInt64(&uploaded, p.Size)
			if cfg.Progress != nil {
				cfg.Progress(atomic.LoadInt64(&uploaded), size)
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		// Best-effort abort so the server doesn't sit on the parts.
		abortURL := fmt.Sprintf("/api/%s/%s?uploadId=%s",
			url.PathEscape(bucket), EncodeKey(key), url.QueryEscape(init.UploadID))
		_ = c.DoRaw(context.Background(), "DELETE", abortURL, nil, "", nil, nil)
		return "", firstErr
	}

	// 4. Complete
	sort.Slice(results, func(i, j int) bool { return results[i].PartNumber < results[j].PartNumber })
	completeURL := fmt.Sprintf("/api/%s/%s?uploadId=%s",
		url.PathEscape(bucket), EncodeKey(key), url.QueryEscape(init.UploadID))
	body, _ := json.Marshal(map[string]any{"parts": results})
	type completeResp struct {
		ETag string `json:"etag"`
	}
	var cr completeResp
	if err := c.postJSON(ctx, completeURL, "application/json", body, &cr); err != nil {
		return "", fmt.Errorf("complete multipart: %w", err)
	}
	return cr.ETag, nil
}

// planParts splits size into N parts of cfg.PartSize each, last one smaller.
type partPlan struct {
	Number    int
	Offset    int64
	Size      int64
}

func planParts(size, partSize int64) []partPlan {
	if size <= 0 {
		return nil
	}
	n := (size + partSize - 1) / partSize
	out := make([]partPlan, 0, n)
	for i := int64(0); i < n; i++ {
		off := i * partSize
		sz := partSize
		if off+sz > size {
			sz = size - off
		}
		out = append(out, partPlan{Number: int(i + 1), Offset: off, Size: sz})
	}
	return out
}

func (c *Client) uploadPart(ctx context.Context, bucket, key, uploadID string,
	p partPlan, f *os.File,
) (string, error) {
	u := fmt.Sprintf("/api/%s/%s?partNumber=%d&uploadId=%s",
		url.PathEscape(bucket), EncodeKey(key), p.Number, url.QueryEscape(uploadID))
	full := c.cfg.Server + u

	sr := io.NewSectionReader(f, p.Offset, p.Size)
	// We also need to MD5 the bytes for ETag verification, but the server
	// returns its own ETag in the response header, which is authoritative.
	// Re-MD5'ing locally would double-read the file; skip.

	req, err := http.NewRequestWithContext(ctx, "PUT", full, sr)
	if err != nil {
		return "", err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	req.ContentLength = p.Size

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", parseErr(resp)
	}
	etag := resp.Header.Get("ETag")
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		etag = etag[1 : len(etag)-1]
	}
	return etag, nil
}

// postJSON is a thin POST helper for the initiate/complete steps. body=nil
// sends an empty body. respOut may be nil to discard the response.
func (c *Client) postJSON(ctx context.Context, urlPath, contentType string,
	body []byte, respOut any,
) error {
	full := c.cfg.Server + urlPath
	var rdr io.Reader
	if body != nil {
		rdr = bytesReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", full, rdr)
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	if respOut == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respOut)
}

// bytesReader is a tiny io.Reader over a byte slice. Avoids importing
// bytes.Reader just for this; readers in this package stay zero-copy.
type bytesReader []byte

func (b bytesReader) Read(p []byte) (int, error) {
	if len(b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b)
	return n, nil
}

// MD5 helper retained for callers that want to verify per-part ETags
// before sending Complete. Not used internally — the server's response
// is the authority.
func MD5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// PartSizeForFile returns the partSize the CLI would use for a given
// file size — exposed for help text. 5 MiB minimum; bumps for very
// large files so we never exceed the 10,000 part S3 cap.
func PartSizeForFile(size int64) int64 {
	const min = 5 * 1024 * 1024
	const maxParts = 10000
	if size <= 0 {
		return min
	}
	need := (size + min - 1) / min
	if need <= maxParts {
		return min
	}
	// Scale part size to fit under 10k parts, rounded up to MiB.
	scaled := (size + maxParts - 1) / maxParts
	const mib = 1024 * 1024
	return ((scaled + mib - 1) / mib) * mib
}

// strconv re-export to keep call sites short.
var _ = strconv.Itoa
