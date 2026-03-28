package forensics

import (
	"reflect"
	"testing"
)

func TestParseNftJSON(t *testing.T) {
	mockJSON := `{
		"nftables": [
			{ "metainfo": { "version": "1.0.1", "release_name": "Fearless Fosdick #3", "nanos": 1648439241512411 } },
			{ "rule": { "family": "inet", "table": "filter", "chain": "input", "handle": 1, "expr": [
				{ "match": { "left": { "payload": { "protocol": "tcp", "field": "dport" } }, "op": "==", "right": 22 } },
				{ "accept": null }
			] } },
			{ "rule": { "family": "inet", "table": "filter", "chain": "input", "handle": 2, "expr": [
				{ "match": { "left": { "payload": { "protocol": "tcp", "field": "dport" } }, "op": "==", "right": [80, 443] } },
				{ "accept": null }
			] } },
			{ "rule": { "family": "inet", "table": "filter", "chain": "input", "handle": 3, "expr": [
				{ "match": { "left": { "payload": { "protocol": "udp", "field": "dport" } }, "op": "==", "right": 53 } },
				{ "accept": null }
			] } },
			{ "rule": { "family": "inet", "table": "filter", "chain": "input", "handle": 4, "expr": [
				{ "match": { "left": { "payload": { "protocol": "tcp", "field": "dport" } }, "op": "==", "right": 8080 } },
				{ "drop": null }
			] } }
		]
	}`

	tcp, udp, err := parseNftJSON([]byte(mockJSON))
	if err != nil {
		t.Fatalf("Failed to parse mock JSON: %v", err)
	}

	t.Logf("Parsed TCP: %v", tcp)
	t.Logf("Parsed UDP: %v", udp)

	expectedTCP := []int{22, 80, 443}
	expectedUDP := []int{53}

	if !reflect.DeepEqual(tcp, expectedTCP) {
		t.Errorf("TCP ports mismatch: expected %v, got %v", expectedTCP, tcp)
	}
	if !reflect.DeepEqual(udp, expectedUDP) {
		t.Errorf("UDP ports mismatch: expected %v, got %v", expectedUDP, udp)
	}
}

func TestParseNftJSON_Empty(t *testing.T) {
	mockJSON := `{"nftables": []}`
	tcp, udp, err := parseNftJSON([]byte(mockJSON))
	if err != nil {
		t.Fatalf("Failed to parse empty JSON: %v", err)
	}
	if len(tcp) != 0 || len(udp) != 0 {
		t.Errorf("Expected empty slices, got tcp=%v, udp=%v", tcp, udp)
	}
}
