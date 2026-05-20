package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/NullLatency/flow-driver/internal/httpclient"
)

// AppConfig defines the application-level overarching configuration.
type AppConfig struct {
	// ListenAddr is the SOCKS5 listening address for the client. E.g., "127.0.0.1:1080"
	ListenAddr string `json:"listen_addr,omitempty"`

	// ClientID identifies this client node to allow multi-tenant folder sharing.
	ClientID string `json:"client_id,omitempty"`

	// StorageType defines the backend ("local", "google", or "saffronbridge").
	StorageType string `json:"storage_type"`

	// LocalDir is the path used when StorageType is "local".
	LocalDir string `json:"local_dir,omitempty"`

	// GoogleFolderID is the Drive Folder ID when StorageType is "google".
	GoogleFolderID string `json:"google_folder_id,omitempty"`

	// RefreshRateMs is the polling (RX) interval in milliseconds for the engine.
	RefreshRateMs int `json:"refresh_rate_ms,omitempty"`

	// FlushRateMs is the gathering (TX) interval in milliseconds for the engine.
	FlushRateMs int `json:"flush_rate_ms,omitempty"`

	// Transport configures the dpi-evasion layer.
	Transport httpclient.TransportConfig `json:"transport,omitempty"`

	// HybridRelay enables the SaffronBridge mode where client uploads requests
	// through Apps Script relay while responses continue via Drive polling.
	HybridRelay HybridRelayConfig `json:"hybrid_relay,omitempty"`
}

// HybridRelayConfig defines options for the Apps Script request relay path.
type HybridRelayConfig struct {
	// AppScriptURL accepts either deployment ID (AKfycb...) or full /exec URL.
	AppScriptURL string `json:"appscript_url,omitempty"`

	// SharedToken is optional bearer token attached to relay HTTP requests.
	SharedToken string `json:"shared_token,omitempty"`

	// FallbackToDrive makes client upload to Drive directly if relay fails.
	FallbackToDrive bool `json:"fallback_to_drive,omitempty"`

	// ExitListenAddr is used by cmd/hybrid-exit as the local HTTP listen address.
	ExitListenAddr string `json:"exit_listen_addr,omitempty"`
}

// Save writes the config back to a JSON file.
func (c *AppConfig) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// Load reads and parses a JSON config file.
func Load(path string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	return &cfg, nil
}
