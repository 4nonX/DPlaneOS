package nvmet

import (
	"encoding/json"
	"os"
)

// LoadExports reads persisted targets from disk. Missing file returns empty slice.
func LoadExports(path string) ([]Export, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Export
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveExports writes targets atomically.
func SaveExports(path string, exports []Export) error {
	if exports == nil {
		exports = []Export{}
	}
	data, err := json.MarshalIndent(exports, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
