package saffronbridge

import (
	"strconv"
	"strings"
	"time"
)

// RelayUpload is the JSON body sent from client to Apps Script relay,
// then forwarded to the hybrid exit HTTP endpoint.
type RelayUpload struct {
	Filename   string `json:"filename"`
	PayloadB64 string `json:"payload_b64"`
}

// RelayUploadBatch groups multiple request mux uploads in one relay call.
// This reduces Apps Script invocation count under bursty traffic.
type RelayUploadBatch struct {
	Uploads []RelayUpload `json:"uploads"`
}

// ExtractClientID parses req-<clientID>-mux-<ts>.bin style names.
func ExtractClientID(filename string) string {
	parts := strings.Split(filename, "-")
	if len(parts) >= 4 && parts[2] == "mux" {
		return parts[1]
	}
	return ""
}

// IsRequestMux returns true for request uploads from client to server side.
func IsRequestMux(filename string) bool {
	return strings.HasPrefix(filename, "req-")
}

// ParseMuxTimestamp extracts the unix-nano timestamp from mux file names like
// req-<client>-mux-<ts>.bin or res-<client>-mux-<ts>.bin.
func ParseMuxTimestamp(filename string) (time.Time, bool) {
	parts := strings.Split(filename, "-")
	if len(parts) < 4 || parts[2] != "mux" {
		return time.Time{}, false
	}
	tsPart := strings.TrimSuffix(parts[3], ".bin")
	ts, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil || ts <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ts), true
}
