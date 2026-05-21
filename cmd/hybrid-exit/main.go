package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NullLatency/flow-driver/internal/config"
	"github.com/NullLatency/flow-driver/internal/httpclient"
	"github.com/NullLatency/flow-driver/internal/hybrid/saffronbridge"
	"github.com/NullLatency/flow-driver/internal/storage"
	"github.com/NullLatency/flow-driver/internal/transport"
)

func main() {
	var configPath, gcPath, listenAddr string
	flag.StringVar(&configPath, "c", "config.json", "Path to config file")
	flag.StringVar(&gcPath, "gc", "credentials.json", "Path to Google OAuth credentials JSON")
	flag.StringVar(&listenAddr, "listen", "", "Hybrid relay listen address (override config)")
	flag.Parse()

	log.Println("Starting SaffronBridge Hybrid Exit...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appCfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	driveTransport := appCfg.Transport
	driveTransport.HostHeader = "www.googleapis.com"
	customHTTPClient := httpclient.NewCustomClient(driveTransport)
	backend := storage.NewGoogleBackend(customHTTPClient, gcPath, appCfg.GoogleFolderID)
	if err := backend.Login(ctx); err != nil {
		log.Fatalf("Backend login failed: %v", err)
	}

	if appCfg.GoogleFolderID == "" {
		log.Println("Zero-Config: Searching for existing Google Drive folder 'Flow-Data'...")
		folderID, err := backend.FindFolder(ctx, "Flow-Data")
		if err != nil {
			log.Fatalf("Failed to search for folder: %v", err)
		}
		if folderID == "" {
			log.Println("Zero-Config: 'Flow-Data' not found. Creating new folder...")
			folderID, err = backend.CreateFolder(ctx, "Flow-Data")
			if err != nil {
				log.Fatalf("Failed to auto-create folder: %v", err)
			}
		}
		appCfg.GoogleFolderID = folderID
		if err := appCfg.Save(configPath); err != nil {
			log.Printf("Warning: Failed to save folder ID to %s: %v", configPath, err)
		}
	}

	engine := transport.NewEngine(backend, false, "")
	ackEnabled := appCfg.TunnelAckEnabled(false)
	engine.SetTunnelAckEnabled(ackEnabled)
	if ackEnabled {
		log.Printf("Tunnel ACK mode: enabled")
	} else {
		log.Printf("Tunnel ACK mode: disabled (latency-first)")
	}
	if appCfg.RefreshRateMs > 0 {
		engine.SetPollRate(appCfg.RefreshRateMs)
	}
	if appCfg.FlushRateMs > 0 {
		engine.SetFlushRate(appCfg.FlushRateMs)
	}

	engine.OnNewSession = func(sessionID, targetAddr string, session *transport.Session) {
		log.Printf("Hybrid exit received session %s destined for %s", sessionID, targetAddr)
		go handleServerConn(sessionID, targetAddr, session, engine)
	}
	engine.Start(ctx)

	if listenAddr == "" {
		listenAddr = appCfg.HybridRelay.ExitListenAddr
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8099"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if appCfg.HybridRelay.SharedToken != "" {
			expected := "Bearer " + appCfg.HybridRelay.SharedToken
			if r.Header.Get("Authorization") != expected {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		uploads, err := decodeRelayUploads(body)
		if err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		for i, upload := range uploads {
			if upload.Filename == "" || upload.PayloadB64 == "" {
				http.Error(w, "filename and payload_b64 are required", http.StatusBadRequest)
				return
			}
			if sentAt, ok := saffronbridge.ParseMuxTimestamp(upload.Filename); ok {
				log.Printf("[hybrid-exit] relay ingress file=%s queue_ms=%d", upload.Filename, time.Since(sentAt).Milliseconds())
			}

			raw, err := base64.StdEncoding.DecodeString(upload.PayloadB64)
			if err != nil {
				http.Error(w, "invalid payload_b64", http.StatusBadRequest)
				return
			}

			clientID := saffronbridge.ExtractClientID(upload.Filename)
			if _, err := engine.IngestMuxReader(bytes.NewReader(raw), clientID); err != nil {
				http.Error(w, fmt.Sprintf("failed to ingest mux at index %d", i), http.StatusBadRequest)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("Hybrid relay listening on %s", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("hybrid relay server failed: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down hybrid exit...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func decodeRelayUploads(body []byte) ([]saffronbridge.RelayUpload, error) {
	var batch saffronbridge.RelayUploadBatch
	if err := json.Unmarshal(body, &batch); err == nil && len(batch.Uploads) > 0 {
		return batch.Uploads, nil
	}

	var single saffronbridge.RelayUpload
	if err := json.Unmarshal(body, &single); err == nil && single.Filename != "" {
		return []saffronbridge.RelayUpload{single}, nil
	}

	return nil, fmt.Errorf("invalid relay payload")
}

func handleServerConn(sessionID, targetAddr string, session *transport.Session, engine *transport.Engine) {
	_ = engine
	defer session.RequestClose()

	conn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("Dial error to %s: %v", targetAddr, err)
		return
	}
	defer conn.Close()

	errCh := make(chan error, 2)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				session.EnqueueTx(buf[:n])
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	go func() {
		for {
			data, ok := <-session.RxChan
			if !ok {
				errCh <- fmt.Errorf("session closed by remote")
				return
			}
			if len(data) == 0 {
				continue
			}
			if _, err := conn.Write(data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	err = <-errCh
	if err != nil && !strings.Contains(err.Error(), "closed") {
		log.Printf("session %s closed: %v", sessionID, err)
	}
}
