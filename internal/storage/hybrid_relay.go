package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/NullLatency/flow-driver/internal/hybrid/saffronbridge"
)

// HybridRelayBackend implements SaffronBridge mode:
// - client request mux files are relayed via Apps Script HTTP endpoint
// - response path remains the normal Drive backend.
type HybridRelayBackend struct {
	drive           Backend
	httpClient      *http.Client
	appScriptURL    string
	sharedToken     string
	fallbackToDrive bool
	enqueueCh       chan relayPendingUpload
	relaySeenOK     bool
}

type relayPendingUpload struct {
	upload     saffronbridge.RelayUpload
	rawPayload []byte
	done       chan error
}

type relayForwardResult struct {
	OK          bool   `json:"ok"`
	RelayStatus int    `json:"relay_status"`
	RelayBody   string `json:"relay_body"`
	Error       string `json:"error"`
}

const (
	// Keep this short to preserve latency while still coalescing bursts.
	relayBatchWindow     = 150 * time.Millisecond
	relayBatchMax        = 16
	relayQueueMaxUploads = 1024
	relayRetryMinBackoff = 500 * time.Millisecond
	relayRetryMaxBackoff = 15 * time.Second
)

func NewHybridRelayBackend(
	drive Backend,
	httpClient *http.Client,
	appScriptURL string,
	sharedToken string,
	fallbackToDrive bool,
) *HybridRelayBackend {
	normalizedURL := saffronbridge.NormalizeAppScriptURL(appScriptURL)
	b := &HybridRelayBackend{
		drive:           drive,
		httpClient:      httpClient,
		appScriptURL:    normalizedURL,
		sharedToken:     sharedToken,
		fallbackToDrive: fallbackToDrive,
		enqueueCh:       make(chan relayPendingUpload, 256),
	}
	go b.batchWorker()
	return b
}

func (b *HybridRelayBackend) Login(ctx context.Context) error {
	return b.drive.Login(ctx)
}

func (b *HybridRelayBackend) Upload(ctx context.Context, filename string, data io.Reader) error {
	if !saffronbridge.IsRequestMux(filename) || b.appScriptURL == "" {
		return b.drive.Upload(ctx, filename, data)
	}

	payload, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("hybrid relay read failed: %w", err)
	}

	reqBody := saffronbridge.RelayUpload{
		Filename:   filename,
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
	}

	pending := relayPendingUpload{
		upload:     reqBody,
		rawPayload: payload,
		done:       make(chan error, 1),
	}

	select {
	case b.enqueueCh <- pending:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-pending.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *HybridRelayBackend) batchWorker() {
	for {
		batch := b.collectBatch(<-b.enqueueCh)
		attempt := 1

		for {
			err := b.sendRelayBatch(batch)
			if err == nil {
				if !b.relaySeenOK {
					log.Printf("[hybrid] Apps Script relay active: %s", b.appScriptURL)
					b.relaySeenOK = true
				}
				b.finishBatch(batch, nil)
				break
			}

			if b.fallbackToDrive {
				log.Printf("[hybrid] relay failed, falling back to Drive for %d upload(s): %v", len(batch), err)
				b.finishBatchWithDriveFallback(batch)
				break
			}

			backoff := relayRetryBackoff(attempt)
			attempt++
			log.Printf("[hybrid] relay failed, queueing %d upload(s), retry in %s: %v", len(batch), backoff, err)

			timer := time.NewTimer(backoff)
			waitDone := false
			for !waitDone {
				enqueue := b.enqueueCh
				if len(batch) >= relayQueueMaxUploads {
					enqueue = nil
				}

				select {
				case p := <-enqueue:
					batch = append(batch, p)
				case <-timer.C:
					waitDone = true
				}
			}
		}
	}
}

func (b *HybridRelayBackend) collectBatch(first relayPendingUpload) []relayPendingUpload {
	batch := []relayPendingUpload{first}
	timer := time.NewTimer(relayBatchWindow)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	for len(batch) < relayBatchMax {
		select {
		case p := <-b.enqueueCh:
			batch = append(batch, p)
		case <-timer.C:
			return batch
		}
	}

	return batch
}

func (b *HybridRelayBackend) finishBatch(batch []relayPendingUpload, err error) {
	for _, item := range batch {
		item.done <- err
	}
}

func (b *HybridRelayBackend) finishBatchWithDriveFallback(batch []relayPendingUpload) {
	for _, item := range batch {
		fbErr := b.drive.Upload(context.Background(), item.upload.Filename, bytes.NewReader(item.rawPayload))
		item.done <- fbErr
	}
}

func relayRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	backoff := relayRetryMinBackoff
	for i := 1; i < attempt; i++ {
		if backoff >= relayRetryMaxBackoff {
			return relayRetryMaxBackoff
		}
		backoff *= 2
	}

	if backoff > relayRetryMaxBackoff {
		return relayRetryMaxBackoff
	}
	return backoff
}

func (b *HybridRelayBackend) sendRelayBatch(batch []relayPendingUpload) error {
	if len(batch) == 0 {
		return nil
	}

	reqBatch := saffronbridge.RelayUploadBatch{Uploads: make([]saffronbridge.RelayUpload, 0, len(batch))}
	for _, item := range batch {
		reqBatch.Uploads = append(reqBatch.Uploads, item.upload)
	}

	encoded, err := json.Marshal(reqBatch)
	if err != nil {
		return fmt.Errorf("hybrid relay batch encode failed: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, b.appScriptURL, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("hybrid relay batch build failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.sharedToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.sharedToken)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		b.closeIdleRelayConnections()
		return fmt.Errorf("hybrid relay batch request failed: %w", err)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b.closeIdleRelayConnections()
		return fmt.Errorf("hybrid relay transport returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var relayRes relayForwardResult
	if err := json.Unmarshal(body, &relayRes); err != nil {
		b.closeIdleRelayConnections()
		return fmt.Errorf("hybrid relay invalid JSON response: %w; body=%s", err, strings.TrimSpace(string(body)))
	}

	if !relayRes.OK || relayRes.RelayStatus < 200 || relayRes.RelayStatus >= 300 {
		b.closeIdleRelayConnections()
		return fmt.Errorf(
			"hybrid relay upstream failed: ok=%t status=%d error=%s body=%s",
			relayRes.OK,
			relayRes.RelayStatus,
			strings.TrimSpace(relayRes.Error),
			strings.TrimSpace(relayRes.RelayBody),
		)
	}

	return nil
}

func (b *HybridRelayBackend) closeIdleRelayConnections() {
	if b.httpClient == nil || b.httpClient.Transport == nil {
		return
	}
	if c, ok := b.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (b *HybridRelayBackend) ListQuery(ctx context.Context, prefix string) ([]string, error) {
	return b.drive.ListQuery(ctx, prefix)
}

func (b *HybridRelayBackend) Download(ctx context.Context, filename string) (io.ReadCloser, error) {
	return b.drive.Download(ctx, filename)
}

func (b *HybridRelayBackend) Delete(ctx context.Context, filename string) error {
	return b.drive.Delete(ctx, filename)
}

func (b *HybridRelayBackend) CreateFolder(ctx context.Context, name string) (string, error) {
	return b.drive.CreateFolder(ctx, name)
}

func (b *HybridRelayBackend) FindFolder(ctx context.Context, name string) (string, error) {
	return b.drive.FindFolder(ctx, name)
}
