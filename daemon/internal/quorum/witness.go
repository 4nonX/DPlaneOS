package quorum

import (
	"bytes"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WitnessScript is served at /api/quorum/witness-setup.sh: the third vote
// for any Linux machine.
//
//go:embed witness-setup.sh
var WitnessScript string

// HdrCode carries the one-time enrollment code.
const HdrCode = "X-DPlane-Quorum-Code"

// The enrollment protocol is plain text so the shell script needs only curl:
//
//	POST /api/quorum/enroll/ca                body: qnetd CA (PEM)
//	  → "cluster: <name>\n" + certificate request (PEM)
//	POST /api/quorum/enroll/cert?address=<ip> body: signed certificate (PEM)
//	  → "ok <name>\n"

// FormatCAResponse renders the answer to step 1.
func FormatCAResponse(cluster string, csr []byte) string {
	return "cluster: " + cluster + "\n" + string(csr)
}

func enrollPost(client *http.Client, nodeURL, path, code string, body []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", strings.TrimRight(nodeURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(HdrCode, code)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s (HTTP %d)", strings.TrimSpace(string(out)), resp.StatusCode)
	}
	return out, nil
}

// sourceAddrTowards returns this machine's address on the route to host.
func sourceAddrTowards(host string) (string, error) {
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	conn, err := net.Dial("udp", net.JoinHostPort(ips[0].String(), "5405"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// SourceAddrTowards is exported for address suggestions in the GUI.
func SourceAddrTowards(host string) (string, error) { return sourceAddrTowards(host) }

// JoinCluster makes this DPlaneOS node the third vote of the cluster that
// issued code, with the same steps as witness-setup.sh. sign signs the
// cluster's certificate request and records the cluster.
func JoinCluster(nodeURL, code string, sign func(cluster string, csr []byte) ([]byte, error)) (string, error) {
	u, err := url.Parse(strings.TrimSpace(nodeURL))
	if err != nil || u.Host == "" {
		return "", errors.New("enter the node's address, e.g. https://nas1.lan")
	}
	addr, err := sourceAddrTowards(u.Hostname())
	if err != nil {
		return "", err
	}
	if err := WitnessEnable(); err != nil {
		return "", err
	}
	ca, err := WitnessCA()
	if err != nil {
		return "", err
	}
	// The one-time code authenticates this exchange; NAS certificates are
	// usually self-signed, so the chain is not verified (as with curl -k in
	// the script).
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := enrollPost(client, nodeURL, "/api/quorum/enroll/ca", code, ca)
	if err != nil {
		return "", err
	}
	first, rest, _ := strings.Cut(string(resp), "\n")
	cluster := strings.TrimPrefix(first, "cluster: ")
	if cluster == first || !nameRe.MatchString(cluster) {
		return "", errors.New("unexpected answer from the node")
	}
	cert, err := sign(cluster, []byte(rest))
	if err != nil {
		return "", err
	}
	if _, err := enrollPost(client, nodeURL, "/api/quorum/enroll/cert?address="+url.QueryEscape(addr), code, cert); err != nil {
		return "", err
	}
	return cluster, nil
}
