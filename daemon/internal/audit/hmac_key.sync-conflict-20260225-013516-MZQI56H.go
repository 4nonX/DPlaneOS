package audit

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// LoadOrCreateAuditKey reads the 32-byte HMAC key from path.
// If the file does not exist it is created with a freshly generated key.
// The key is never exposed via any API endpoint.
//
// Call once at daemon startup; pass the result to NewBufferedLogger.
func LoadOrCreateAuditKey(path string) ([]byte, error) {
	// Try to read existing key
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("audit key at %s has wrong length %d (want 32)", path, len(data))
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading audit key: %w", err)
	}

	// Generate new key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating audit key: %w", err)
	}

	// Write with restrictive permissions — root-only read
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating audit key dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("writing audit key: %w", err)
	}

	return key, nil
}
