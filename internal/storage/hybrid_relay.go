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

const (
	// Keep this short to preserve latency while still coalescing bursts.
	relayBatchWindow = 150 * time.Millisecond
	relayBatchMax    = 16
	relayMaxAttempts = 3
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
		first := <-b.enqueueCh
		batch := []relayPendingUpload{first}
		timer := time.NewTimer(relayBatchWindow)

	collect:
		for len(batch) < relayBatchMax {
			select {
			case p := <-b.enqueueCh:
				batch = append(batch, p)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		err := b.sendRelayBatch(batch)
		if err == nil && !b.relaySeenOK {
			log.Printf("[hybrid] Apps Script relay active: %s", b.appScriptURL)
			b.relaySeenOK = true
		}
		if err != nil && b.fallbackToDrive {
			log.Printf("[hybrid] relay failed, falling back to Drive for %d upload(s): %v", len(batch), err)
			for _, item := range batch {
				fbErr := b.drive.Upload(context.Background(), item.upload.Filename, bytes.NewReader(item.rawPayload))
				item.done <- fbErr
			}
			continue
		}

		for _, item := range batch {
			item.done <- err
		}
	}
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

	var lastErr error
	for attempt := 1; attempt <= relayMaxAttempts; attempt++ {
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
			lastErr = fmt.Errorf("hybrid relay batch request failed: %w", err)
			b.closeIdleRelayConnections()
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("hybrid relay batch returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		b.closeIdleRelayConnections()
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("hybrid relay batch failed: unknown error")
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
