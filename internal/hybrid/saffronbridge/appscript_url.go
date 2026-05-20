package saffronbridge

import (
	"fmt"
	"strings"
)

// NormalizeAppScriptURL accepts either:
// 1) deployment ID (AKfycb...)
// 2) full Apps Script /exec URL
// and returns a canonical /exec URL.
func NormalizeAppScriptURL(raw string) string {
	id := normalizeDeploymentID(raw)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("https://script.google.com/macros/s/%s/exec", id)
}

func normalizeDeploymentID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}

	v = strings.TrimSuffix(v, "/exec")
	v = strings.Trim(v, "/")
	parts := strings.Split(v, "/")
	if len(parts) >= 2 {
		for i := 0; i < len(parts)-1; i++ {
			if parts[i] == "s" {
				return parts[i+1]
			}
		}
	}
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return v
}
