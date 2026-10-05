package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"dplaned/internal/cmdutil"
)

// CTDBConfig represents CTDB clustering configuration for Samba HA
type CTDBConfig struct {
	Enable           bool     `json:"enable"`
	DataPool         string   `json:"data_pool"`
	DataDataset      string   `json:"data_dataset"`
	PublicAddresses  []string `json:"public_addresses"`
	NodeTimeout      int      `json:"node_timeout"`
	RecoveryTimeout  int      `json:"recovery_timeout"`
	LogLevel         int      `json:"log_level"`
}

// CTDBNodeStatus represents status of a single CTDB node
type CTDBNodeStatus struct {
	NodeID     int    `json:"node_id"`
	Address    string `json:"address"`
	Status     string `json:"status"`      // connected | disconnected
	Role       string `json:"role"`        // leader | member
	VnnMap     int    `json:"vnn_map"`     // virtual node number mapping
	Flags      string `json:"flags"`       // node flags
	Generation uint64 `json:"generation"` // generation counter
}

// CTDBDatabaseStatus represents CTDB persistent/volatile database status
type CTDBDatabaseStatus struct {
	DatabaseName      string `json:"database_name"`
	Nodes             int    `json:"nodes_count"`
	ReplicationStatus string `json:"replication_status"`
	OutOfSync         []int  `json:"out_of_sync_nodes"`
}

// CTDBStatusResponse is the full CTDB cluster status response
type CTDBStatusResponse struct {
	Success           bool                 `json:"success"`
	Enabled           bool                 `json:"enabled"`
	DaemonRunning     bool                 `json:"daemon_running"`
	ClusterStatus     string               `json:"cluster_status"` // healthy | degraded | unhealthy
	Nodes             []CTDBNodeStatus     `json:"nodes"`
	DatabaseStates    []CTDBDatabaseStatus `json:"databases"`
	PublicIPStatus    map[string]string    `json:"public_ips"`     // IP -> owning_node_id
	ErrorMessage      string               `json:"error,omitempty"`
}

// HandleCTDBGetConfig returns current CTDB configuration
func HandleCTDBGetConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var cfg CTDBConfig
		err := db.QueryRow(`
			SELECT enable, data_pool, data_dataset, node_timeout, recovery_timeout, log_level
			FROM ctdb_config WHERE id = 1
		`).Scan(&cfg.Enable, &cfg.DataPool, &cfg.DataDataset, &cfg.NodeTimeout, &cfg.RecoveryTimeout, &cfg.LogLevel)

		if err == sql.ErrNoRows {
			// Return defaults
			cfg = CTDBConfig{
				Enable:          false,
				DataPool:        "tank",
				DataDataset:     "tank/ctdb",
				NodeTimeout:     30,
				RecoveryTimeout: 120,
				LogLevel:        2,
			}
		} else if err != nil {
			http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
			return
		}

		// Fetch public addresses (separate table)
		rows, err := db.Query(`SELECT address FROM ctdb_public_addresses WHERE config_id = 1 ORDER BY idx`)
		if err == nil {
			defer rows.Close()
			cfg.PublicAddresses = make([]string, 0)
			for rows.Next() {
				var addr string
				if err := rows.Scan(&addr); err == nil {
					cfg.PublicAddresses = append(cfg.PublicAddresses, addr)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"config":  cfg,
		})
	}
}

// HandleCTDBSetConfig updates CTDB configuration
func HandleCTDBSetConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var cfg CTDBConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
			return
		}

		// Validate
		if cfg.NodeTimeout < 10 {
			http.Error(w, "node_timeout must be >= 10 seconds", http.StatusBadRequest)
			return
		}
		if cfg.RecoveryTimeout < 30 {
			http.Error(w, "recovery_timeout must be >= 30 seconds", http.StatusBadRequest)
			return
		}
		if cfg.LogLevel < 0 || cfg.LogLevel > 4 {
			http.Error(w, "log_level must be 0-4", http.StatusBadRequest)
			return
		}

		// Save config
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, fmt.Sprintf("Transaction error: %v", err), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		_, err = tx.Exec(`
			INSERT INTO ctdb_config (id, enable, data_pool, data_dataset, node_timeout, recovery_timeout, log_level)
			VALUES (1, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO UPDATE SET
				enable = excluded.enable,
				data_pool = excluded.data_pool,
				data_dataset = excluded.data_dataset,
				node_timeout = excluded.node_timeout,
				recovery_timeout = excluded.recovery_timeout,
				log_level = excluded.log_level
		`, cfg.Enable, cfg.DataPool, cfg.DataDataset, cfg.NodeTimeout, cfg.RecoveryTimeout, cfg.LogLevel)

		if err != nil {
			http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
			return
		}

		// Clear and set public addresses
		tx.Exec(`DELETE FROM ctdb_public_addresses WHERE config_id = 1`)
		for i, addr := range cfg.PublicAddresses {
			tx.Exec(`
				INSERT INTO ctdb_public_addresses (config_id, idx, address)
				VALUES (1, $1, $2)
			`, i, addr)
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, fmt.Sprintf("Commit error: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("CTDB configuration saved (enable=%v)", cfg.Enable)

		// Start or stop the service; report a failure instead of claiming success.
		var svcErr error
		if cfg.Enable {
			if out, err := cmdutil.RunMedium("systemctl_ctdb_enable_start", "enable", "ctdb"); err != nil {
				svcErr = fmt.Errorf("enable ctdb: %v %s", err, strings.TrimSpace(string(out)))
			} else if out, err := cmdutil.RunMedium("systemctl_ctdb_start", "start", "ctdb"); err != nil {
				svcErr = fmt.Errorf("start ctdb: %v %s", err, strings.TrimSpace(string(out)))
			}
		} else if out, err := cmdutil.RunMedium("systemctl_ctdb_stop", "stop", "ctdb"); err != nil {
			svcErr = fmt.Errorf("stop ctdb: %v %s", err, strings.TrimSpace(string(out)))
		}
		if svcErr != nil {
			log.Printf("CTDB: %v", svcErr)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Configuration saved, but " + svcErr.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "CTDB configuration updated",
		})
	}
}

// HandleCTDBStatus returns real-time CTDB cluster status
func HandleCTDBStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resp := CTDBStatusResponse{Success: true}

		// Check if CTDB is enabled
		var enabled bool
		err := db.QueryRow(`SELECT enable FROM ctdb_config WHERE id = 1`).Scan(&enabled)
		if err == sql.ErrNoRows {
			enabled = false
		}
		resp.Enabled = enabled

		if !enabled {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Check if daemon is running
		err = exec.Command("systemctl", "is-active", "ctdb").Run()
		resp.DaemonRunning = err == nil

		if !resp.DaemonRunning {
			resp.ClusterStatus = "offline"
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Query ctdb status (ctdb nodestatus)
		out, err := cmdutil.RunFast("ctdb_nodestatus", "ctdb", "nodestatus")
		if err != nil {
			resp.ClusterStatus = "error"
			resp.ErrorMessage = err.Error()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Parse nodestatus output
		resp.Nodes = parseCtdbNodeStatus(string(out))

		// Determine overall cluster status
		healthyCount := 0
		for _, node := range resp.Nodes {
			if node.Status == "connected" {
				healthyCount++
			}
		}
		if healthyCount == len(resp.Nodes) {
			resp.ClusterStatus = "healthy"
		} else if healthyCount > 0 {
			resp.ClusterStatus = "degraded"
		} else {
			resp.ClusterStatus = "unhealthy"
		}

		// Query public IP status (ctdb ip)
		out, err = cmdutil.RunFast("ctdb_ip", "ctdb", "ip")
		if err == nil {
			resp.PublicIPStatus = parseCtdbIPs(string(out))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// parseCtdbNodeStatus parses output of "ctdb nodestatus" command
func parseCtdbNodeStatus(output string) []CTDBNodeStatus {
	// Example output:
	// |Node|IP|Disconnected|Banned|Disabled|Unhealthy|Stopped|Inactive|PartiallyOnline|Generation|
	// |0|192.168.1.1|0|0|0|0|0|0|0|5|
	// |1|192.168.1.2|0|0|0|0|0|0|0|5|

	var nodes []CTDBNodeStatus
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i, line := range lines {
		if i == 0 || !strings.Contains(line, "|") {
			continue // skip header
		}

		parts := strings.Split(line, "|")
		if len(parts) < 10 {
			continue
		}

		nodeID := strings.TrimSpace(parts[1])
		address := strings.TrimSpace(parts[2])
		disconnected := strings.TrimSpace(parts[3]) == "1"

		status := "connected"
		if disconnected {
			status = "disconnected"
		}

		nodes = append(nodes, CTDBNodeStatus{
			NodeID:  parseIntDefault(nodeID, -1),
			Address: address,
			Status:  status,
			Role:    "member", // would need additional query to determine leader
		})
	}

	return nodes
}

// parseCtdbIPs parses output of "ctdb ip" command
func parseCtdbIPs(output string) map[string]string {
	// Example output:
	// Public IPs on node 0
	// 192.168.1.100 node[0] active
	// 192.168.1.101 node[1] active

	ips := make(map[string]string)
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			ip := parts[0]
			owner := strings.TrimSpace(parts[1])
			ips[ip] = owner
		}
	}

	return ips
}

// parseIntDefault parses string to int with default on error
func parseIntDefault(s string, def int) int {
	var i int
	if _, err := fmt.Sscanf(s, "%d", &i); err != nil {
		return def
	}
	return i
}

// HandleCTDBDatabaseStatus returns status of CTDB databases
func HandleCTDBDatabaseStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Check if CTDB is enabled
		var enabled bool
		db.QueryRow(`SELECT enable FROM ctdb_config WHERE id = 1`).Scan(&enabled)
		if !enabled {
			http.Error(w, "CTDB is not enabled", http.StatusBadRequest)
			return
		}

		// Query ctdb dbstatus
		out, err := cmdutil.RunFast("ctdb_dbstatus", "ctdb", "dbstatus")
		if err != nil {
			http.Error(w, fmt.Sprintf("CTDB error: %v", err), http.StatusInternalServerError)
			return
		}

		dbs := parseCtdbDatabaseStatus(string(out))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"databases": dbs,
		})
	}
}

// parseCtdbDatabaseStatus parses "ctdb dbstatus" output
func parseCtdbDatabaseStatus(output string) []CTDBDatabaseStatus {
	var dbs []CTDBDatabaseStatus
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if strings.Contains(line, "Database") {
			// Parse database name and status
			parts := strings.Split(line, " ")
			if len(parts) >= 2 {
				dbName := strings.TrimSpace(parts[1])
				status := "unknown"
				if strings.Contains(line, "OK") {
					status = "ok"
				} else if strings.Contains(line, "UNHEALTHY") {
					status = "unhealthy"
				}

				dbs = append(dbs, CTDBDatabaseStatus{
					DatabaseName:      dbName,
					ReplicationStatus: status,
				})
			}
		}
	}

	return dbs
}

// Schema initialization for CTDB config tables
func InitCTDBSchema(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ctdb_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enable BOOLEAN NOT NULL DEFAULT FALSE,
			data_pool TEXT NOT NULL DEFAULT 'tank',
			data_dataset TEXT NOT NULL DEFAULT 'tank/ctdb',
			node_timeout INTEGER NOT NULL DEFAULT 30,
			recovery_timeout INTEGER NOT NULL DEFAULT 120,
			log_level INTEGER NOT NULL DEFAULT 2
		)
	`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ctdb_public_addresses (
			config_id INTEGER NOT NULL,
			idx INTEGER NOT NULL,
			address TEXT NOT NULL,
			PRIMARY KEY (config_id, idx),
			FOREIGN KEY (config_id) REFERENCES ctdb_config(id) ON DELETE CASCADE
		)
	`); err != nil {
		return err
	}

	// Ensure defaults exist
	db.Exec(`
		INSERT INTO ctdb_config (id, enable, data_pool, data_dataset, node_timeout, recovery_timeout, log_level)
		VALUES (1, false, 'tank', 'tank/ctdb', 30, 120, 2)
		ON CONFLICT (id) DO NOTHING
	`)

	return nil
}
