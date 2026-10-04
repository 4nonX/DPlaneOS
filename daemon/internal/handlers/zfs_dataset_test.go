package handlers

import "testing"

func TestDatasetCreateOpts(t *testing.T) {
	for _, cs := range []string{"", "sensitive"} {
		opts, err := datasetCreateOpts(cs)
		if err != nil || opts != nil {
			t.Errorf("datasetCreateOpts(%q) = %v, %v; want nil, nil", cs, opts, err)
		}
	}
	opts, err := datasetCreateOpts("insensitive")
	if err != nil || opts["casesensitivity"] != "insensitive" || len(opts) != 1 {
		t.Errorf("datasetCreateOpts(insensitive) = %v, %v", opts, err)
	}
	if _, err := datasetCreateOpts("INSENSITIVE"); err == nil {
		t.Error("expected error for unknown value")
	}
}
