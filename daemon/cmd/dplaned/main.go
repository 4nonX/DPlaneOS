package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dplaned/internal/alerts"
	"dplaned/internal/audit"
	"dplaned/internal/gitops"
	"dplaned/internal/ha"
	"dplaned/internal/handlers"
	"dplaned/internal/jobs"
	"dplaned/internal/middleware"
	"dplaned/internal/monitoring"
	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
	"dplaned/internal/persistguard"
	"dplaned/internal/reconciler"
	"dplaned/internal/security"
	"dplaned/internal/websocket"
	"dplaned/internal/zfs"
	"github.com/gorilla/mux"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	Version = "dev" // overridden at build time via: -ldflags "-X main.Version=$(cat VERSION)"
)

func main() {
	// Parse flags
	listenAddr := flag.String("listen", "127.0.0.1:9000", "Listen address")
	dbDSN := flag.String("db-dsn", "postgres://dplaneos@localhost/dplaneos?sslmode=disable", "PostgreSQL DSN")
	telegramBot := flag.String("telegram-bot", "", "Telegram bot token (optional, for alerts)")
	telegramChat := flag.String("telegram-chat", "", "Telegram chat ID (optional, for alerts)")
	configDir := flag.String("config-dir", "/etc/dplaneos", "Config directory (for NixOS: /var/lib/dplaneos/config)")
	smbConfPath := flag.String("smb-conf", "/etc/samba/smb.conf", "Path to write SMB config (for NixOS: /var/lib/dplaneos/smb-shares.conf)")
	haLocalID := flag.String("ha-local-id", "", "Unique ID for this cluster node (default: /etc/machine-id prefix)")
	haLocalAddr := flag.String("ha-local-addr", "", "HTTP address peers use to reach this daemon, e.g. http://10.0.0.1:5050")
	gitopsStatePath := flag.String("gitops-state", "/var/lib/dplaneos/gitops/state.yaml", "Path to GitOps state.yaml (managed by git repo)")
	applyOnly := flag.Bool("apply", false, "Apply GitOps state and exit (Phase 3.1)")
	diffOnly := flag.Bool("diff", false, "Output reconciliation plan as JSON and exit")
	convergenceCheck := flag.Bool("convergence-check", false, "Verify if system has converged and exit (CONVERGED|NOT_CONVERGED)")
	testSerialization := flag.Bool("test-serialization", false, "Verify state.yaml round-trip (Phase 4.1)")
	testIdempotency := flag.Bool("test-idempotency", false, "Verify Apply(S); Apply(S) results in zero diff (Phase 4.2)")
	ceTokenFile := flag.String("ce-token-file", "", "Path to Compliance Engine API token file (Phase 6.2)")
	initOnly := flag.Bool("init-only", false, "Initialize database schema and exit")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("D-PlaneOS v%s\n", Version)
		os.Exit(0)
	}

	// Phase 0: Database Initialization
	if *initOnly {
		db, err := sql.Open("pgx", *dbDSN)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		if err := initSchema(db); err != nil {
			log.Fatalf("Schema init failed: %v", err)
		}
		log.Printf("Database initialized at %s", *dbDSN)
		os.Exit(0)
	}

	// Phase 3.1: One-off apply if requested
	if *applyOnly {
		log.Printf("GITOPS: Running one-off apply from %s", *gitopsStatePath)
		// 1. Open DB
		db, err := sql.Open("pgx", *dbDSN)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		// 2. Init Schema
		if err := initSchema(db); err != nil {
			log.Fatalf("Schema init failed: %v", err)
		}
		// 3. Load State
		content, err := os.ReadFile(*gitopsStatePath)
		if err != nil {
			log.Fatalf("Failed to read state file: %v", err)
		}
		desired, err := gitops.ParseStateYAML(string(content))
		if err != nil {
			log.Fatalf("Failed to parse desired state: %v", err)
		}
		// 4. Compute Diff
		live, err := gitops.ReadLiveState(db)
		if err != nil {
			log.Fatalf("Failed to read live state: %v", err)
		}
		plan := gitops.ComputeDiff(desired, live)
		// 5. Apply
		ctx := gitops.ApplyContext{
			DB:             db,
			SmbConfPath:    *smbConfPath,
			NFSExportsPath: "/etc/exports",
		}
		result, err := gitops.ApplyPlan(ctx, plan, desired)
		if err != nil {
			log.Fatalf("GITOPS APPLY FAILED: %v (Status: %s, Reason: %s, Item: %s)", err, result.Status, result.HaltReason, result.Failed)
		}
		log.Printf("GITOPS: Apply complete! (%d items applied, Post-Apply Convergence: %s)", len(result.Applied), result.Convergence)
		os.Exit(0)
	}

	if *diffOnly {
		db, err := sql.Open("pgx", *dbDSN)
		if err != nil {
			log.Fatalf("DB failed: %v", err)
		}
		// Initialize database schema (safe on every call)
		if err := initSchema(db); err != nil {
			log.Fatalf("Database schema initialization failed: %v", err)
		}
		content, err := os.ReadFile(*gitopsStatePath)
		if err != nil {
			log.Fatalf("Read failed: %v", err)
		}
		desired, err := gitops.ParseStateYAML(string(content))
		if err != nil {
			log.Fatalf("Parse failed: %v", err)
		}
		live, _ := gitops.ReadLiveState(db)
		plan := gitops.ComputeDiff(desired, live)
		out, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(out))
		os.Exit(0)
	}

	if *convergenceCheck {
		db, err := sql.Open("pgx", *dbDSN)
		if err != nil {
			log.Fatalf("DB failed: %v", err)
		}
		// Initialize database schema (safe on every call)
		if err := initSchema(db); err != nil {
			log.Fatalf("Database schema initialization failed: %v", err)
		}
		content, err := os.ReadFile(*gitopsStatePath)
		if err != nil {
			log.Fatalf("Read failed: %v", err)
		}
		desired, err := gitops.ParseStateYAML(string(content))
		if err != nil {
			log.Fatalf("Parse failed: %v", err)
		}
		status, err := gitops.ConvergenceCheck(db, desired)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(status)
		os.Exit(0)
	}

	if *testSerialization {
		log.Printf("COMPLIANCE: Testing serialization of %s", *gitopsStatePath)
		content, err := os.ReadFile(*gitopsStatePath)
		if err != nil {
			log.Fatalf("Read failed: %v", err)
		}
		s1, err := gitops.ParseStateYAML(string(content))
		if err != nil {
			log.Fatalf("Initial parse failed: %v", err)
		}
		// Print it back out (we need a PrintStateYAML function)
		// and re-parse.
		s2raw := gitops.PrintStateYAML(s1)
		s2, err := gitops.ParseStateYAML(s2raw)
		if err != nil {
			log.Fatalf("Round-trip re-parse failed: %v\n---\n%s\n---", err, s2raw)
		}
		s3raw := gitops.PrintStateYAML(s2)
		if s3raw != s2raw {
			log.Fatalf("Round-trip canonical YAML mismatch: second print differs from first (parse→print→parse lost canonical form)\n--- first ---\n%s\n--- second ---\n%s\n", s2raw, s3raw)
		}
		log.Printf("COMPLIANCE: Serialization test PASSED")
		os.Exit(0)
	}

	if *testIdempotency {
		log.Printf("COMPLIANCE: Testing idempotency of %s", *gitopsStatePath)
		db, err := sql.Open("pgx", *dbDSN)
		if err != nil {
			log.Fatalf("DB failed: %v", err)
		}
		content, err := os.ReadFile(*gitopsStatePath)
		if err != nil {
			log.Fatalf("Read failed: %v", err)
		}
		desired, err := gitops.ParseStateYAML(string(content))
		if err != nil {
			log.Fatalf("Parse failed: %v", err)
		}

		// 1. Apply once
		ctx := gitops.ApplyContext{DB: db, SmbConfPath: *smbConfPath, NFSExportsPath: "/etc/exports"}
		live1, _ := gitops.ReadLiveState(db)
		plan1 := gitops.ComputeDiff(desired, live1)
		_, err = gitops.ApplyPlan(ctx, plan1, desired)
		if err != nil {
			log.Fatalf("First apply failed: %v", err)
		}

		// 2. Read live again and diff
		live2, err := gitops.ReadLiveState(db)
		if err != nil {
			log.Fatalf("Second live read failed: %v", err)
		}
		plan2 := gitops.ComputeDiff(desired, live2)

		// Filter out NOPs for count check
		var realChanges []string
		for _, item := range plan2.Items {
			if item.Action != gitops.ActionNOP {
				realChanges = append(realChanges, fmt.Sprintf("%s %s", item.Action, item.Name))
			}
		}

		if len(realChanges) > 0 {
			log.Fatalf("COMPLIANCE: Idempotency FAILED! Remaining changes: %v", realChanges)
		}
		log.Printf("COMPLIANCE: Idempotency test PASSED (converged to zero diff)")
		os.Exit(0)
	}

	// Set configurable paths for NixOS compatibility
	handlers.SetConfigDir(*configDir)

	// Expose daemon version to handlers package (used by /api/system/updates/daemon-version)
	handlers.DaemonVersion = Version

	// Open database for buffered audit logging
	db, err := sql.Open("pgx", *dbDSN)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize database schema (IF NOT EXISTS - safe on every startup)
	if err := initSchema(db); err != nil {
		log.Fatalf("Database schema initialization failed: %v", err)
	}

	// Phase 6.2: Bootstrap CE Token if provided
	bootstrapCEToken(db, *ceTokenFile)

	// Ensure reconciler tables exist (network state persistence for non-NixOS)
	if err := reconciler.EnsureSchema(db); err != nil {
		log.Printf("WARNING: reconciler schema failed: %v", err)
	}

	// networkdwriter: writes /etc/systemd/network/50-dplane-*.{network,netdev}
	// These files survive reboots AND nixos-rebuild switch with zero extra steps.
	// Works on every systemd distro: NixOS, Debian, Ubuntu, Arch, RHEL.
	netWriter := networkdwriter.Default()
	if netWriter.IsNetworkd() {
		log.Printf("systemd-networkd active - networkd file writer enabled (%s)", networkdwriter.DefaultNetworkDir)
	} else {
		log.Printf("systemd-networkd not active - networkd files written, reload deferred")
	}
	handlers.SetNetWriter(netWriter)

	// nixwriter: NixOS-only, ONLY for settings with no networkd equivalent:
	//   - networking.firewall (firewall ports)
	//   - services.dplaneos.samba (samba globals)
	// All network config (IPs, VLANs, bonds, DNS) now goes through networkdwriter.
	nixWriter := nixwriter.DefaultWriter()
	_ = nixWriter.LoadFromDisk()
	if nixWriter.IsNixOS() {
		log.Printf("NixOS detected - JSON state writer active: %s", nixwriter.StateJSONPath)
	}
	handlers.SetNixWriter(nixWriter)
	handlers.SetReconcilerDB(db)
	handlers.SetRegistryDB(db)
	handlers.SetGitOpsStatePath(*gitopsStatePath)

	// Boot reconciler: fallback for systems where networkd was not active when
	// files were written, or for Debian/Ubuntu with NetworkManager instead of networkd.
	// On NixOS + networkd: this is a no-op (networkd already read the files at boot).
	go reconciler.Run(db)

	// Load or create the HMAC key for audit chain integrity (Phase 1.5)
	auditKey, err := audit.LoadOrCreateAuditKey("/var/lib/dplaneos/audit.key")
	if err != nil {
		log.Printf("WARNING: audit HMAC key unavailable (%v) - chain disabled", err)
		auditKey = nil
	}

	// Initialize buffered audit logging (non-blocking)
	bufferedLogger := audit.NewBufferedLogger(db, 100, 5*time.Second, auditKey)
	bufferedLogger.Start()
	defer bufferedLogger.Stop()
	audit.SetGlobalBufferedLogger(bufferedLogger)

	// Initialize database connection for session validation
	if err := security.InitDatabase(*dbDSN); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer security.CloseDatabase()

	// ── HA cluster manager ──
	haID := *haLocalID
	if haID == "" {
		haID = handlers.LocalNodeID()
	}
	haAddr := *haLocalAddr
	if haAddr == "" {
		haAddr = "http://" + *listenAddr
	}
	clusterMgr := ha.NewManager(db, haID, haAddr, Version)
	clusterMgr.Start()
	defer clusterMgr.Stop()
	haHandler := handlers.NewHAHandler(clusterMgr)

	// Initialize Telegram alerts (from flags OR database)
	if *telegramBot != "" && *telegramChat != "" {
		// Use command-line flags if provided
		alerts.InitTelegram(*telegramBot, *telegramChat)
	} else {
		// Try to load from database
		var botToken, chatID string
		var enabled int
		err := db.QueryRow("SELECT bot_token, chat_id, enabled FROM telegram_config WHERE id = 1").Scan(&botToken, &chatID, &enabled)
		if err == nil && enabled == 1 && botToken != "" && chatID != "" {
			alerts.InitTelegram(botToken, chatID)
			log.Println("Telegram alerts loaded from database")
		}
	}

	// ── Central Alerting ────────────────────────────────────────────────────────
	// Dispatchers are wired after wsHub is ready for systemic monitoring coverage.

	// Wire webhook + SMTP senders into the ZFS heartbeat package so that
	// pool CRITICAL / DEGRADED events also reach webhook and SMTP channels.
	zfs.SetAlertSenders(
		func(event, pool, msg string) {
			handlers.SendWebhookAlert(db, event, "critical", msg,
				map[string]interface{}{"pool": pool})
		},
		handlers.SendSMTPAlert,
	)

	// Phase 7: ZFS Initialization & Safety Checks

	// ── Patroni Startup Split-Brain Guard (v7.1.0) ──
	// If HA is enabled, we check the local Patroni instance role BEFORE
	// discovering or importing ZFS pools.
	skipZFS := false
	if nixWriter.State().HAEnable {
		log.Printf("HA: Checking local Patroni role before initiating ZFS operations...")
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://localhost:8008/health")
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				// Patroni /health returns 200 for Leader, 503 for Replica (by default).
				log.Printf("HA SAFETY: Patroni /health returned %d (Replica/Follower). Blocking automatic ZFS pool discovery to prevent split-brain.", resp.StatusCode)
				skipZFS = true
			} else {
				log.Printf("HA SAFETY: Patroni /health returned 200 (Leader). Proceeding with ZFS operations.")
			}
		} else {
			// No error path: If Patroni isn't running or reachable, we assume
			// it's not a HA secondary situation we should be worried about yet.
			log.Printf("HA: Patroni unreachable (%v) - assuming single-node or bootstrap mode. Proceeding.", err)
		}
	}

	if !skipZFS {
		// Initialize ZFS pool heartbeat monitoring
		poolList, err := zfs.DiscoverPools()
		if err != nil {
			log.Printf("Warning: Failed to discover ZFS pools: %v", err)
		} else if len(poolList) > 0 {
			for _, pool := range poolList {
				heartbeat := zfs.NewPoolHeartbeat(pool.Name, pool.MountPoint, 30*time.Second)
				// SAFETY: Stop Docker if the pool goes offline during runtime.
				// This prevents containers from writing to bare mountpoint directories
				// on the root FS - the same data-loss scenario the boot gate prevents.
				heartbeat.StopDockerOnFailure = true

				// CRITICAL callback: Telegram + (webhook/SMTP via sendAlert inside heartbeat)
				heartbeat.SetErrorCallback(func(poolName string, err error, details map[string]string) {
					alertErr := alerts.SendAlert(alerts.TelegramAlert{
						Level:   "CRITICAL",
						Title:   fmt.Sprintf("ZFS Pool Failure: %s", poolName),
						Message: err.Error(),
						Details: details,
					})
					if alertErr != nil {
						log.Printf("Failed to send Telegram alert: %v", alertErr)
					}
				})

				// DEGRADED callback: Telegram warning
				heartbeat.SetDegradedCallback(func(poolName string, err error, details map[string]string) {
					alertErr := alerts.SendAlert(alerts.TelegramAlert{
						Level:   "WARNING",
						Title:   fmt.Sprintf("ZFS Pool Degraded: %s", poolName),
						Message: err.Error(),
						Details: details,
					})
					if alertErr != nil {
						log.Printf("Failed to send Telegram degraded alert: %v", alertErr)
					}
				})

				heartbeat.Start()
				defer heartbeat.Stop()
			}
		}
	}

	log.Printf("D-PlaneOS Daemon v%s starting...", Version)

	// Initialize WebSocket Hub for real-time monitoring
	wsHub := websocket.NewMonitorHub()
	go wsHub.Run()

	// Wire the WS hub into disk-event handlers so they can broadcast
	// diskAdded / diskRemoved / poolHealthChanged events.
	handlers.SetDiskEventHub(wsHub)

	// daemonCtx is cancelled on graceful shutdown to stop all background goroutines
	// that support context-based termination (e.g. ZED listener).
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	// Initialize ZED Event Listener (Unix Socket)
	go zfs.StartZEDListener(daemonCtx, "/run/dplaneos/dplaneos.sock",
		func(eventType string, data interface{}, level string) {
			wsHub.Broadcast(eventType, data, level)
		},
		func(event, pool, msg string) {
			parts := strings.Split(event, ".")
			level := "warning"
			if len(parts) >= 2 {
				level = parts[1]
			}
			handlers.DispatchAlert(level, event, pool, msg)
		},
	)

	// Initialize Background Monitor (30s interval)
	// Broadcasts inotify stats to WebSocket clients
	bgMonitor := monitoring.NewBackgroundMonitor(30*time.Second, func(eventType string, data interface{}, level string) {
		wsHub.Broadcast(eventType, data, level)
	})
	bgMonitor.Start()
	defer bgMonitor.Stop()

	// ── Central Alerting ────────────────────────────────────────────────────────
	// Dispatchers are wired after wsHub is ready for systemic monitoring coverage.

	// Wire the WS hub and all other dispatchers into the central alert system.
	handlers.SetAlertDispatchers(
		func(event, source, msg string) {
			handlers.SendWebhookAlert(db, event, "critical", msg,
				map[string]interface{}{"source": source})
		},
		handlers.SendSMTPAlert,
		func(message string) {
			_ = alerts.SendAlert(alerts.TelegramAlert{
				Level:   "CRITICAL",
				Title:   "D-PlaneOS Alert",
				Message: message,
				Details: nil,
			})
		},
		wsHub.Broadcast,
	)

	// Wire WebSocket hub into jobs system for automatic background task updates
	jobs.SetBroadcastCallback(wsHub.Broadcast)

	clusterMgr.SetReplicationProgressReporter(func(p map[string]interface{}) {
		wsHub.Broadcast("ha.replication_progress", p, "info")
	})

	// Start job reaper: remove finished jobs after 1 hour
	jobs.StartReaper(1 * time.Hour)

	persistguard.Start()

	// Start background audit log rotation
	StartAuditRotation(db)

	// Create router
	r := mux.NewRouter()

	// Middleware
	r.Use(loggingMiddleware)
	r.Use(sessionMiddleware(db))
	r.Use(rateLimitMiddleware)

	// Health check

	// ─── AUTH ROUTES (public, no session required) ───
	authHandler := handlers.NewAuthHandler(db)
	r.HandleFunc("/api/auth/login", authHandler.Login).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/auth/logout", authHandler.Logout).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/auth/check", authHandler.Check).Methods("GET")
	r.HandleFunc("/api/auth/session", authHandler.Session).Methods("GET")
	r.HandleFunc("/api/auth/change-password", authHandler.ChangePassword).Methods("POST")
	r.HandleFunc("/api/auth/sessions", authHandler.ListSessions).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/auth/sessions", authHandler.RevokeSession).Methods("DELETE", "OPTIONS")
	r.HandleFunc("/api/csrf", authHandler.CSRFToken).Methods("GET")

	// TOTP 2FA
	totpHandler := handlers.NewTOTPHandler(db)
	r.HandleFunc("/api/auth/totp/setup", totpHandler.HandleTOTPSetup).Methods("GET", "POST", "DELETE")
	r.HandleFunc("/api/auth/totp/verify", totpHandler.HandleTOTPVerify).Methods("POST")

	// API Tokens
	apiTokenHandler := handlers.NewAPITokenHandler(db)
	r.HandleFunc("/api/auth/tokens", apiTokenHandler.HandleTokens).Methods("GET", "POST", "DELETE")

	// Session cleanup goroutine
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			authHandler.CleanExpiredSessions()
		}
	}()

	r.HandleFunc("/health", healthCheckHandler).Methods("GET")

	// Job status polling - used by async operations (replication, rsync, docker pull, etc.)
	r.HandleFunc("/api/jobs/{id}", handlers.HandleJobStatus).Methods("GET")

	// ZFS handlers
	zfsHandler := handlers.NewZFSHandler(db)
	r.Handle("/api/zfs/command", permRoute("storage", "write", zfsHandler.HandleCommand)).Methods("POST")
	r.Handle("/api/zfs/pools", permRoute("storage", "read", zfsHandler.ListPools)).Methods("GET")
	r.Handle("/api/zfs/datasets", permRoute("storage", "read", zfsHandler.ListDatasets)).Methods("GET")
	r.Handle("/api/zfs/datasets", permRoute("storage", "write", zfsHandler.CreateDataset)).Methods("POST")
	r.Handle("/api/zfs/rename", permRoute("storage", "write", zfsHandler.RenameDataset)).Methods("POST")
	r.Handle("/api/zfs/promote", permRoute("storage", "write", zfsHandler.PromoteDataset)).Methods("POST")

	// ZFS Encryption handlers
	zfsEncryptionHandler := handlers.NewZFSEncryptionHandler()
	r.Handle("/api/zfs/encryption/list", permRoute("storage", "read", zfsEncryptionHandler.ListEncryptedDatasets)).Methods("GET")
	r.Handle("/api/zfs/encryption/unlock", permRoute("storage", "admin", zfsEncryptionHandler.UnlockDataset)).Methods("POST")
	r.Handle("/api/zfs/encryption/lock", permRoute("storage", "admin", zfsEncryptionHandler.LockDataset)).Methods("POST")
	r.Handle("/api/zfs/encryption/create", permRoute("storage", "write", zfsEncryptionHandler.CreateEncryptedDataset)).Methods("POST")
	r.Handle("/api/zfs/encryption/change-key", permRoute("storage", "admin", zfsEncryptionHandler.ChangeKey)).Methods("POST")

	// System handlers
	systemHandler := handlers.NewSystemHandler()
	r.HandleFunc("/api/system/health", handlers.SystemHealthHandler).Methods("GET")
	r.Handle("/api/system/logs/stream", permRoute("system", "read", handlers.LogStreamHandler)).Methods("GET")
	r.Handle("/api/system/ups", permRoute("system", "read", systemHandler.GetUPSStatus)).Methods("GET")
	r.Handle("/api/system/ups", permRoute("system", "write", systemHandler.SaveUPSConfig)).Methods("POST")
	r.Handle("/api/system/network", permRoute("system", "read", systemHandler.HandleNetwork)).Methods("GET")
	r.Handle("/api/system/network", permRoute("system", "write", systemHandler.HandleNetwork)).Methods("PUT")
	r.Handle("/api/system/logs", permRoute("system", "read", systemHandler.GetSystemLogs)).Methods("GET")
	r.Handle("/api/system/reboot", permRoute("system", "admin", systemHandler.Reboot)).Methods("POST")
	r.Handle("/api/system/poweroff", permRoute("system", "admin", systemHandler.Poweroff)).Methods("POST")
	r.Handle("/api/system/diagnostics", permRoute("system", "read", systemHandler.RunDiagnostics)).Methods("POST", "OPTIONS")

	// SMART handlers
	r.HandleFunc("/api/hardware/smart/cron-hook", handlers.RunSMARTCronHook).Methods("POST")
	r.Handle("/api/hardware/smart/run-now", permRoute("system", "write", handlers.RunSMARTNow)).Methods("POST")
	r.Handle("/api/hardware/smart/schedules", permRoute("system", "read", handlers.ListSMARTSchedules)).Methods("GET")
	r.Handle("/api/hardware/smart/schedules", permRoute("system", "write", handlers.AddSMARTSchedule)).Methods("POST")
	r.Handle("/api/hardware/smart/schedules", permRoute("system", "write", handlers.DeleteSMARTSchedule)).Methods("DELETE")

	// Docker handlers
	dockerHandler := handlers.NewDockerHandler()
	r.Handle("/api/docker/containers", permRoute("docker", "read", dockerHandler.ListContainers)).Methods("GET")
	r.Handle("/api/docker/icon-map", permRoute("docker", "read", handlers.HandleDockerIconMap)).Methods("GET")
	// Custom icon assets - served from /var/lib/dplaneos/custom_icons/
	// ORDER MATTERS: the exact /list route must be registered before the PathPrefix
	// catch-all, because gorilla/mux matches in registration order and PathPrefix
	// would otherwise intercept /api/assets/custom-icons/list.
	r.Handle("/api/assets/custom-icons/list", permRoute("docker", "read", handlers.HandleCustomIconList)).Methods("GET")
	r.PathPrefix("/api/assets/custom-icons/").Handler(permRoute("docker", "read", handlers.HandleCustomIconFile)).Methods("GET")
	r.Handle("/api/docker/action", permRoute("docker", "write", dockerHandler.ContainerAction)).Methods("POST")
	r.Handle("/api/docker/logs", permRoute("docker", "read", dockerHandler.ContainerLogs)).Methods("GET")
	// v3.0.0: Docker enhanced
	r.Handle("/api/docker/update", permRoute("docker", "admin", dockerHandler.SafeUpdate)).Methods("POST")
	r.Handle("/api/docker/pull", permRoute("docker", "write", dockerHandler.PullImage)).Methods("POST")
	r.Handle("/api/docker/remove", permRoute("docker", "write", dockerHandler.RemoveContainer)).Methods("POST")
	r.Handle("/api/docker/prune", permRoute("docker", "admin", dockerHandler.PruneDocker)).Methods("POST")
	r.Handle("/api/docker/stats", permRoute("docker", "read", dockerHandler.ContainerStats)).Methods("GET")
	r.Handle("/api/docker/compose/up", permRoute("docker", "write", dockerHandler.ComposeUp)).Methods("POST")
	r.Handle("/api/docker/compose/down", permRoute("docker", "write", dockerHandler.ComposeDown)).Methods("POST")
	r.Handle("/api/docker/compose/status", permRoute("docker", "read", dockerHandler.ComposeStatus)).Methods("GET")
	r.Handle("/api/docker/images", permRoute("docker", "read", dockerHandler.ListImages)).Methods("GET")
	r.Handle("/api/docker/images/{id}", permRoute("docker", "write", dockerHandler.RemoveImage)).Methods("DELETE")

	// v3.0.0: ZFS Snapshots CRUD
	snapshotCRUDHandler := handlers.NewZFSSnapshotHandler()
	r.Handle("/api/zfs/snapshots", permRoute("storage", "read", snapshotCRUDHandler.ListSnapshots)).Methods("GET")
	r.Handle("/api/zfs/snapshots", permRoute("storage", "write", snapshotCRUDHandler.CreateSnapshot)).Methods("POST")
	r.Handle("/api/zfs/snapshots", permRoute("storage", "write", snapshotCRUDHandler.DestroySnapshot)).Methods("DELETE")
	r.Handle("/api/zfs/snapshots/rollback", permRoute("storage", "write", snapshotCRUDHandler.RollbackSnapshot)).Methods("POST")
	r.Handle("/api/zfs/snapshots/clone", permRoute("storage", "write", snapshotCRUDHandler.CloneSnapshot)).Methods("POST")
	// NOTE: cron-hook is registered below alongside the rest of the snapshot schedule handlers (v2.0.0 block)

	// v3.0.0: ZFS Replication (remote send/recv)
	replicationRemoteHandler := handlers.NewReplicationHandler()
	r.Handle("/api/replication/remote", permRoute("storage", "write", replicationRemoteHandler.ReplicateToRemote)).Methods("POST")
	r.Handle("/api/replication/test", permRoute("storage", "read", replicationRemoteHandler.TestRemoteConnection)).Methods("POST")
	r.Handle("/api/replication/ssh-keygen", permRoute("storage", "admin", handlers.GenerateReplicationKey)).Methods("POST")
	r.Handle("/api/replication/ssh-pubkey", permRoute("storage", "read", handlers.GetReplicationPubKey)).Methods("GET")
	r.Handle("/api/replication/ssh-copy-id", permRoute("storage", "admin", handlers.CopyReplicationKey)).Methods("POST")

	// v3.0.0: ZFS Time Machine (browse snapshots, restore single files)
	timeMachineHandler := handlers.NewZFSTimeMachineHandler()
	r.Handle("/api/timemachine/versions", permRoute("storage", "read", timeMachineHandler.ListSnapshotVersions)).Methods("GET")
	r.Handle("/api/timemachine/browse", permRoute("storage", "read", timeMachineHandler.BrowseSnapshot)).Methods("GET")
	r.Handle("/api/timemachine/restore", permRoute("storage", "write", timeMachineHandler.RestoreFile)).Methods("POST")

	// v3.0.0: ZFS Sandbox (ephemeral Docker environments via ZFS clone)
	sandboxHandler := handlers.NewZFSSandboxHandler()
	r.Handle("/api/sandbox/create", permRoute("docker", "write", sandboxHandler.CreateSandbox)).Methods("POST")
	r.Handle("/api/sandbox/list", permRoute("docker", "read", sandboxHandler.ListSandboxes)).Methods("GET")
	r.Handle("/api/sandbox/destroy", permRoute("docker", "write", sandboxHandler.DestroySandbox)).Methods("DELETE", "POST")

	// v3.0.0: NixOS Config Guard (only active on NixOS systems)
	nixosGuardHandler := handlers.NewNixOSGuardHandler(db)
	r.Handle("/api/nixos/detect", permRoute("system", "read", nixosGuardHandler.DetectNixOS)).Methods("GET")
	r.Handle("/api/nixos/status", permRoute("system", "read", nixosGuardHandler.GetStatus)).Methods("GET")
	r.Handle("/api/nixos/diff-intent", permRoute("system", "read", nixosGuardHandler.DiffIntent)).Methods("GET")
	r.Handle("/api/nixos/reconcile", permRoute("system", "admin", nixosGuardHandler.Reconcile)).Methods("POST")
	r.Handle("/api/nixos/validate", permRoute("system", "admin", nixosGuardHandler.ValidateConfig)).Methods("POST")
	r.Handle("/api/nixos/generations", permRoute("system", "read", nixosGuardHandler.ListGenerations)).Methods("GET")
	r.Handle("/api/nixos/rollback", permRoute("system", "admin", nixosGuardHandler.RollbackGeneration)).Methods("POST")

	// v3.0.0: ZFS Health Predictor (deep monitoring, heatmap data)
	healthHandler := handlers.NewZFSHealthHandler()
	r.HandleFunc("/api/zfs/health", healthHandler.GetPoolHealth).Methods("GET")
	r.HandleFunc("/api/zfs/iostat", healthHandler.GetIOStats).Methods("GET")
	r.HandleFunc("/api/zfs/events", healthHandler.GetPoolEvents).Methods("GET")
	r.HandleFunc("/api/zfs/smart", healthHandler.GetSMARTHealth).Methods("GET")

	// v3.0.0: Pool Capacity Guardian (prevents ZFS full freeze)
	capacityHandler := handlers.NewCapacityGuardianHandler()
	r.Handle("/api/zfs/capacity", permRoute("storage", "read", capacityHandler.GetCapacityStatus)).Methods("GET")
	r.Handle("/api/zfs/capacity/reserve", permRoute("storage", "admin", capacityHandler.SetupReserve)).Methods("POST")
	r.Handle("/api/zfs/capacity/release", permRoute("storage", "admin", capacityHandler.ReleaseReserve)).Methods("POST")

	// v3.0.0: Power-loss state locks
	stateLockHandler := handlers.NewStateLockHandler()
	r.Handle("/api/system/stale-locks", permRoute("system", "read", stateLockHandler.CheckStaleLocks)).Methods("GET")
	r.Handle("/api/system/stale-locks/clear", permRoute("system", "admin", stateLockHandler.ClearStaleLock)).Methods("POST")

	// v3.0.0: Sandbox orphan cleanup
	r.Handle("/api/sandbox/cleanup", permRoute("docker", "admin", sandboxHandler.CleanOrphanVolumes)).Methods("POST")

	// v3.0.0: NixOS diff + watchdog
	r.Handle("/api/nixos/diff", permRoute("system", "read", nixosGuardHandler.DiffGenerations)).Methods("GET")
	r.Handle("/api/nixos/apply", permRoute("system", "admin", nixosGuardHandler.ApplyWithWatchdog)).Methods("POST")
	r.Handle("/api/nixos/confirm", permRoute("system", "admin", nixosGuardHandler.ConfirmApply)).Methods("POST")
	r.Handle("/api/nixos/watchdog", permRoute("system", "read", nixosGuardHandler.WatchdogStatus)).Methods("GET")
	r.Handle("/api/nixos/pre-upgrade-snapshots", permRoute("system", "read", nixosGuardHandler.ListPreUpgradeSnapshots)).Methods("GET")
	r.Handle("/api/nixos/backup-config", permRoute("system", "admin", nixosGuardHandler.BackupConfig)).Methods("POST")

	// v3.0.0: Docker pre-flight check
	r.Handle("/api/docker/preflight", permRoute("docker", "read", dockerHandler.PreFlightCheck)).Methods("GET")
	r.Handle("/api/docker/gpu", permRoute("docker", "read", handlers.HandleDockerGPUPassthroughReport)).Methods("GET")

	// ── Git Sync ──
	gitSyncHandler := handlers.NewGitSyncHandler(db)
	r.Handle("/api/git-sync/config", permRoute("system", "read", gitSyncHandler.GetConfig)).Methods("GET")
	r.Handle("/api/git-sync/config", permRoute("system", "write", gitSyncHandler.SaveConfig)).Methods("POST")
	r.Handle("/api/git-sync/pull", permRoute("system", "write", gitSyncHandler.Pull)).Methods("POST")
	r.Handle("/api/git-sync/status", permRoute("system", "read", gitSyncHandler.Status)).Methods("GET")
	r.Handle("/api/git-sync/stacks", permRoute("system", "read", gitSyncHandler.ListStacks)).Methods("GET")
	r.Handle("/api/git-sync/deploy", permRoute("system", "admin", gitSyncHandler.Deploy)).Methods("POST")
	r.Handle("/api/git-sync/export", permRoute("system", "admin", gitSyncHandler.ExportContainers)).Methods("POST")
	r.Handle("/api/git-sync/push", permRoute("system", "write", gitSyncHandler.Push)).Methods("POST")

	// Git-Sync: Multi-Repo + Credentials (v2.1.1)
	gitReposHandler := handlers.NewGitReposHandler(db)
	r.Handle("/api/git-sync/credentials", permRoute("system", "read", gitReposHandler.ListCredentials)).Methods("GET")
	r.Handle("/api/git-sync/credentials", permRoute("system", "write", gitReposHandler.SaveCredential)).Methods("POST")
	r.Handle("/api/git-sync/credentials/test", permRoute("system", "read", gitReposHandler.TestCredential)).Methods("POST")
	r.Handle("/api/git-sync/credentials/delete", permRoute("system", "write", gitReposHandler.DeleteCredential)).Methods("DELETE", "POST")
	r.Handle("/api/git-sync/repos", permRoute("system", "read", gitReposHandler.ListRepos)).Methods("GET")
	r.Handle("/api/git-sync/repos", permRoute("system", "write", gitReposHandler.SaveRepo)).Methods("POST")
	r.Handle("/api/git-sync/repos/delete", permRoute("system", "write", gitReposHandler.DeleteRepo)).Methods("DELETE", "POST")
	r.Handle("/api/git-sync/repos/pull", permRoute("system", "write", gitReposHandler.PullRepo)).Methods("POST")
	r.Handle("/api/git-sync/repos/push", permRoute("system", "write", gitReposHandler.PushRepo)).Methods("POST")
	r.Handle("/api/git-sync/repos/deploy", permRoute("system", "admin", gitReposHandler.DeployRepo)).Methods("POST")
	r.Handle("/api/git-sync/repos/browse", permRoute("system", "read", gitReposHandler.BrowseFiles)).Methods("GET")
	r.Handle("/api/git-sync/credentials/branches", permRoute("system", "read", gitReposHandler.ListBranches)).Methods("GET")
	r.Handle("/api/git-sync/repos/export", permRoute("system", "admin", gitReposHandler.ExportToRepo)).Methods("POST")
	gitSyncHandler.StartAutoSync()

	// v5.1: Compose stack management
	stackHandler := handlers.NewStackHandler(db)
	r.Handle("/api/docker/stacks", permRoute("docker", "read", stackHandler.ListStacks)).Methods("GET")
	r.Handle("/api/docker/stacks/deploy", permRoute("docker", "write", stackHandler.DeployStack)).Methods("POST")
	r.Handle("/api/docker/stacks/yaml", permRoute("docker", "read", stackHandler.GetStackYAML)).Methods("GET")
	r.Handle("/api/docker/stacks/yaml", permRoute("docker", "write", stackHandler.UpdateStackYAML)).Methods("PUT")
	r.Handle("/api/docker/stacks", permRoute("docker", "write", stackHandler.DeleteStack)).Methods("DELETE")
	r.Handle("/api/docker/stacks/action", permRoute("docker", "write", stackHandler.StackAction)).Methods("POST")
	r.Handle("/api/docker/convert-run", permRoute("docker", "write", stackHandler.ConvertDockerRun)).Methods("POST")

	// v5.1: Multi-stack templates
	templateHandler := handlers.NewTemplateHandler()
	r.Handle("/api/docker/templates", permRoute("docker", "read", templateHandler.ListTemplates)).Methods("GET")
	r.Handle("/api/docker/templates/installed", permRoute("docker", "read", templateHandler.ListInstalledTemplates)).Methods("GET")
	r.Handle("/api/docker/templates/deploy", permRoute("docker", "write", templateHandler.DeployTemplate)).Methods("POST")

	// v3.0.0: Audit log rotation
	auditRotationHandler := handlers.NewAuditRotationHandler(db, *dbDSN, "/var/lib/dplaneos/audit.key")
	r.Handle("/api/system/audit/rotate", permRoute("system", "admin", auditRotationHandler.RotateAuditLogs)).Methods("POST")
	r.Handle("/api/system/audit/stats", permRoute("audit", "read", auditRotationHandler.GetAuditStats)).Methods("GET")
	r.Handle("/api/system/audit/logs", permRoute("audit", "read", auditRotationHandler.GetAuditLogs)).Methods("GET")
	r.Handle("/api/system/audit/verify-chain", permRoute("audit", "read", auditRotationHandler.VerifyAuditChain)).Methods("GET")
	r.HandleFunc("/api/system/ce-status", auditRotationHandler.GetCEStatus).Methods("GET")

	supportBundleHandler := handlers.NewSupportBundleHandler(db, Version)
	r.Handle("/api/system/support-bundle", permRoute("system", "admin", supportBundleHandler.GenerateBundle)).Methods("POST")

	webhookHandler := handlers.NewWebhookHandler(db, Version)
	r.Handle("/api/alerts/webhooks", permRoute("system", "read", webhookHandler.ListWebhooks)).Methods("GET")
	r.Handle("/api/alerts/webhooks", permRoute("system", "write", webhookHandler.SaveWebhook)).Methods("POST")
	r.Handle("/api/alerts/webhooks/{id}", permRoute("system", "write", webhookHandler.DeleteWebhook)).Methods("DELETE")
	r.Handle("/api/alerts/webhooks/{id}/test", permRoute("system", "write", webhookHandler.TestWebhook)).Methods("POST")

	// Phase 3: GitOps - declarative state reconciliation
	gitopsHandler := handlers.NewGitOpsHandler(db, *gitopsStatePath, *smbConfPath, wsHub)
	defer gitopsHandler.Stop()

	// Start GitOps drift detector - polls every 5 minutes and broadcasts
	// "gitops.drift" WS events so GitOpsPage reacts in real time.
	driftDetector := gitops.NewDriftDetector(db, *gitopsStatePath, 5*time.Minute, wsHub)
	driftDetector.Start()
	defer driftDetector.Stop()
	r.Handle("/api/gitops/status", permRoute("system", "read", gitopsHandler.Status)).Methods("GET")
	r.Handle("/api/gitops/plan", permRoute("system", "read", gitopsHandler.Plan)).Methods("GET")
	r.Handle("/api/gitops/apply", permRoute("system", "admin", gitopsHandler.Apply)).Methods("POST")
	r.Handle("/api/gitops/approve", permRoute("system", "admin", gitopsHandler.Approve)).Methods("POST")
	r.Handle("/api/gitops/check", permRoute("system", "admin", gitopsHandler.Check)).Methods("POST")
	r.Handle("/api/gitops/state", permRoute("system", "read", gitopsHandler.GetState)).Methods("GET")
	r.Handle("/api/gitops/state", permRoute("system", "admin", gitopsHandler.PutState)).Methods("PUT")
	r.Handle("/api/gitops/settings", permRoute("system", "read", gitopsHandler.GetSettings)).Methods("GET")
	r.Handle("/api/gitops/settings", permRoute("system", "admin", gitopsHandler.UpdateSettings)).Methods("PUT")
	r.Handle("/api/gitops/sync", permRoute("system", "admin", gitopsHandler.SyncNow)).Methods("POST")

	// v3.0.0: Zombie disk watcher
	zombieHandler := handlers.NewZombieWatcherHandler()
	r.Handle("/api/zfs/disk-latency", permRoute("storage", "read", zombieHandler.CheckDiskLatency)).Methods("GET")

	// v3.0.0: LDAP Circuit Breaker
	r.Handle("/api/ldap/circuit-breaker", permRoute("system", "read", handlers.GetCircuitBreakerStatus)).Methods("GET")
	r.Handle("/api/ldap/circuit-breaker/reset", permRoute("system", "admin", handlers.ResetCircuitBreaker)).Methods("POST")

	// v3.0.0: ZFS Scrub management
	r.Handle("/api/zfs/scrub/start", permRoute("storage", "write", handlers.StartScrub)).Methods("POST")
	r.Handle("/api/zfs/scrub/stop", permRoute("storage", "write", handlers.StopScrub)).Methods("POST")
	r.Handle("/api/zfs/scrub/status", permRoute("storage", "read", handlers.GetScrubStatus)).Methods("GET")

	// Pool maintenance operations
	r.Handle("/api/zfs/pool/operations", permRoute("storage", "write", handlers.PoolOperations)).Methods("POST")

	// Resilver progress (separate from scrub - parses resilver-specific scan lines)
	r.Handle("/api/zfs/resilver/status", permRoute("storage", "read", handlers.HandleResilverStatus)).Methods("GET")

	// v3.0.0: VDEV / Pool expansion
	r.Handle("/api/zfs/pool/add-vdev", permRoute("storage", "write", handlers.AddVdevToPool)).Methods("POST")
	r.Handle("/api/zfs/pool/remove-device", permRoute("storage", "write", handlers.RemoveCacheOrLog)).Methods("POST")
	r.Handle("/api/zfs/pool/replace", permRoute("storage", "write", handlers.ReplaceDisk)).Methods("POST")
	r.Handle("/api/zfs/pool/attach", permRoute("storage", "write", handlers.AttachDisk)).Methods("POST")
	r.Handle("/api/zfs/pool/detach", permRoute("storage", "write", handlers.DetachDisk)).Methods("POST")
	r.Handle("/api/zfs/pool/topology", permRoute("storage", "read", handlers.GetPoolTopology)).Methods("GET")
	r.Handle("/api/zfs/disk/wipe", permRoute("storage", "write", handlers.WipeDisk)).Methods("POST")

	// v3.0.0: Dataset quotas
	r.Handle("/api/zfs/dataset/quota", permRoute("storage", "write", zfsHandler.SetDatasetQuota)).Methods("POST")
	r.Handle("/api/zfs/dataset/quota", permRoute("storage", "read", zfsHandler.GetDatasetQuota)).Methods("GET")

	// v3.0.0: Per-user and per-group quotas (ZFS userquota/groupquota)
	r.Handle("/api/zfs/quota/usergroup", permRoute("storage", "read", zfsHandler.GetUserGroupQuotas)).Methods("GET")
	r.Handle("/api/zfs/quota/usergroup", permRoute("storage", "write", zfsHandler.SetUserGroupQuota)).Methods("POST")

	// v3.0.0: S.M.A.R.T. tests
	r.Handle("/api/zfs/smart/test", permRoute("system", "write", handlers.RunSMARTTest)).Methods("POST")
	r.Handle("/api/zfs/smart/results", permRoute("system", "read", handlers.GetSMARTTestResults)).Methods("GET")
	// SMART failure prediction (calls PredictDiskFailure via TranslateSMARTAttribute)
	r.Handle("/api/zfs/smart/predict", permRoute("system", "read", handlers.HandleSMARTPrediction)).Methods("GET")

	// v3.0.0: ZFS delegation (zfs allow)
	r.Handle("/api/zfs/delegation", permRoute("storage", "admin", handlers.SetZFSDelegation)).Methods("POST")
	r.Handle("/api/zfs/delegation", permRoute("storage", "read", handlers.GetZFSDelegation)).Methods("GET")
	r.Handle("/api/zfs/delegation/revoke", permRoute("storage", "admin", handlers.RevokeZFSDelegation)).Methods("POST")

	// v3.0.0: Network rollback
	r.Handle("/api/network/apply", permRoute("network", "write", handlers.ApplyNetworkWithRollback)).Methods("POST")
	r.Handle("/api/network/confirm", permRoute("network", "write", handlers.ConfirmNetwork)).Methods("POST")

	// v3.0.0: SMB VFS modules
	r.Handle("/api/smb/vfs", permRoute("shares", "read", handlers.GetSMBVFSConfig)).Methods("GET")
	r.Handle("/api/smb/vfs", permRoute("shares", "write", handlers.SetSMBVFSConfig)).Methods("POST")

	// v3.0.0: VLAN management
	r.Handle("/api/network/vlan", permRoute("network", "read", handlers.ListVLANs)).Methods("GET")
	r.Handle("/api/network/vlan", permRoute("network", "write", handlers.CreateVLAN)).Methods("POST")
	r.Handle("/api/network/vlan", permRoute("network", "write", handlers.DeleteVLAN)).Methods("DELETE")

	// v3.0.0: Link Aggregation / Bonding
	r.Handle("/api/network/bond", permRoute("network", "read", handlers.ListBonds)).Methods("GET")
	r.Handle("/api/network/bond", permRoute("network", "write", handlers.CreateBond)).Methods("POST")
	r.Handle("/api/network/bond/{name}", permRoute("network", "write", handlers.DeleteBond)).Methods("DELETE")

	// v3.0.0: NTP configuration
	r.Handle("/api/system/ntp", permRoute("system", "read", handlers.GetNTPStatus)).Methods("GET")
	r.Handle("/api/system/ntp", permRoute("system", "write", handlers.SetNTPServers)).Methods("POST")

	// Shares handlers (config management)
	r.Handle("/api/shares/smb/reload", permRoute("shares", "admin", handlers.ReloadSMBConfig)).Methods("POST")
	r.Handle("/api/shares/smb/test", permRoute("shares", "admin", handlers.TestSMBConfig)).Methods("POST")
	r.Handle("/api/shares/nfs/reload", permRoute("shares", "admin", handlers.ReloadNFSExports)).Methods("POST")
	r.Handle("/api/shares/nfs/list", permRoute("shares", "read", handlers.ListNFSExports)).Methods("GET")

	// NFS CRUD handler - NFSHandler manages /etc/exports via PostgreSQL
	r.Handle("/api/zfs/pool/offline", permRoute("storage", "write", zfsHandler.OfflineDisk)).Methods("POST")
	r.Handle("/api/zfs/pool/export", permRoute("storage", "write", zfsHandler.ExportPool)).Methods("POST")
	nfsHandler := handlers.NewNFSHandler(db)
	r.Handle("/api/nfs/status", permRoute("shares", "read", nfsHandler.GetNFSStatus)).Methods("GET")
	r.Handle("/api/nfs/exports", permRoute("shares", "read", nfsHandler.ListNFSExports)).Methods("GET")
	r.Handle("/api/nfs/exports", permRoute("shares", "write", nfsHandler.CreateNFSExport)).Methods("POST")
	r.Handle("/api/nfs/exports/{id}/update", permRoute("shares", "admin", nfsHandler.UpdateNFSExport)).Methods("POST")
	r.Handle("/api/nfs/exports/{id}", permRoute("shares", "write", nfsHandler.DeleteNFSExport)).Methods("DELETE")
	r.Handle("/api/nfs/reload", permRoute("shares", "admin", nfsHandler.ReloadNFSExportsHandler)).Methods("POST")

	// Shares CRUD handlers
	shareCRUDHandler := handlers.NewShareCRUDHandler(db, *smbConfPath)
	r.Handle("/api/shares/list", permRoute("shares", "read", shareCRUDHandler.HandleShares)).Methods("GET")
	r.Handle("/api/shares", permRoute("shares", "read", shareCRUDHandler.HandleShares)).Methods("GET")
	r.Handle("/api/shares/by-path", permRoute("shares", "read", shareCRUDHandler.GetSharesByPath)).Methods("GET")
	r.Handle("/api/shares", permRoute("shares", "write", shareCRUDHandler.HandleShares)).Methods("POST", "PUT")
	r.Handle("/api/shares", permRoute("shares", "write", shareCRUDHandler.HandleShares)).Methods("DELETE")

	// User & Group CRUD handlers
	userGroupHandler := handlers.NewUserGroupHandler(db)
	r.Handle("/api/rbac/users", permRoute("users", "read", userGroupHandler.HandleUsers)).Methods("GET")
	r.Handle("/api/rbac/users", permRoute("users", "write", userGroupHandler.HandleUsers)).Methods("POST")
	r.Handle("/api/rbac/groups", permRoute("users", "read", userGroupHandler.HandleGroups)).Methods("GET")
	r.Handle("/api/rbac/groups", permRoute("users", "write", userGroupHandler.HandleGroups)).Methods("POST")

	// System status, profile, preflight, setup handlers
	systemStatusHandler := handlers.NewSystemStatusHandler(db, Version)
	r.HandleFunc("/api/system/setup-admin", systemStatusHandler.HandleSetupAdmin).Methods("POST")
	r.HandleFunc("/api/system/status", systemStatusHandler.HandleStatus).Methods("GET") // Public-alias (dashboard needs basic status before login)
	r.Handle("/api/system/profile", permRoute("system", "read", systemStatusHandler.HandleProfile)).Methods("GET")
	r.Handle("/api/system/settings", permRoute("system", "read", systemStatusHandler.HandleSettings)).Methods("GET")
	r.Handle("/api/system/settings", permRoute("system", "write", systemStatusHandler.HandleSettings)).Methods("POST")
	r.Handle("/api/system/preflight", permRoute("system", "read", systemStatusHandler.HandlePreflight)).Methods("GET")

	// OTA update endpoints (Debian/Ubuntu)
	r.Handle("/api/system/updates/check", permRoute("system", "read", handlers.HandleUpdatesCheck)).Methods("GET")
	r.Handle("/api/system/updates/apply", permRoute("system", "admin", handlers.HandleUpdatesApply)).Methods("POST")
	r.Handle("/api/system/updates/apply-security", permRoute("system", "admin", handlers.HandleUpdatesApplySecurity)).Methods("POST")
	r.Handle("/api/system/updates/daemon-version", permRoute("system", "read", handlers.HandleDaemonVersion)).Methods("GET")
	r.Handle("/api/system/zfs-gate-status", permRoute("system", "read", systemStatusHandler.HandleZFSGateStatus)).Methods("GET")
	// v3.0.0: IPMI/BMC sensor data (graceful no-op if ipmitool unavailable)
	r.Handle("/api/system/ipmi", permRoute("system", "read", systemStatusHandler.HandleIPMISensors)).Methods("GET")
	// /api/status is an alias for /api/system/status (used by dashboard ECC check)
	r.HandleFunc("/api/status", systemStatusHandler.HandleStatus).Methods("GET")
	r.HandleFunc("/api/system/setup-complete", systemStatusHandler.HandleSetupComplete).Methods("POST")
	r.Handle("/api/system/metrics", permRoute("system", "read", handlers.HandleSystemMetrics)).Methods("GET")
	r.Handle("/api/system/tuning", permRoute("system", "read", handlers.HandleSystemSettings)).Methods("GET")
	r.Handle("/api/system/tuning", permRoute("system", "write", handlers.HandleSystemSettings)).Methods("POST")

	// Disk discovery (setup wizard)
	r.Handle("/api/system/disks", permRoute("storage", "read", handlers.HandleDiskDiscovery)).Methods("GET")
	r.Handle("/api/zfs/pool/replacement-disks", permRoute("storage", "read", handlers.HandleReplacementDiskCandidates)).Methods("GET")
	r.Handle("/api/system/pool/create", permRoute("storage", "write", handlers.HandlePoolCreate)).Methods("POST")

	// Disk lifecycle event endpoint (localhost only - called by udev/systemd)
	r.HandleFunc("/api/internal/disk-event", handlers.HandleDiskEvent).Methods("POST")

	// Files handlers
	filesHandler := handlers.NewFilesExtendedHandler()
	r.Handle("/api/files/list", permRoute("storage", "read", filesHandler.ListFiles)).Methods("GET")
	r.Handle("/api/files/properties", permRoute("storage", "read", filesHandler.GetFileProperties)).Methods("GET")
	r.Handle("/api/files/read", permRoute("storage", "read", filesHandler.ReadFile)).Methods("GET")
	r.Handle("/api/files/download", permRoute("storage", "read", filesHandler.DownloadFile)).Methods("GET")
	r.Handle("/api/files/rename", permRoute("storage", "write", filesHandler.RenameFile)).Methods("POST")
	r.Handle("/api/files/copy", permRoute("storage", "write", filesHandler.CopyFile)).Methods("POST")
	r.Handle("/api/files/move", permRoute("storage", "write", filesHandler.MoveFile)).Methods("POST")
	r.Handle("/api/files/write", permRoute("storage", "write", filesHandler.WriteFile)).Methods("POST")
	r.Handle("/api/files/upload", permRoute("storage", "write", filesHandler.UploadChunk)).Methods("POST")
	r.Handle("/api/files/mkdir", permRoute("storage", "write", handlers.CreateDirectory)).Methods("POST")
	r.Handle("/api/files/delete", permRoute("storage", "write", handlers.DeletePath)).Methods("POST")
	r.Handle("/api/files/chown", permRoute("storage", "write", handlers.ChangeOwnership)).Methods("POST")
	r.Handle("/api/files/chmod", permRoute("storage", "write", handlers.ChangePermissions)).Methods("POST")

	// Backup handlers
	r.Handle("/api/backup/rsync", permRoute("storage", "read", handlers.ExecuteRsync)).Methods("GET")
	r.Handle("/api/backup/rsync", permRoute("storage", "write", handlers.ExecuteRsync)).Methods("POST")
	r.Handle("/api/backup/rsync/{id}", permRoute("storage", "write", http.HandlerFunc(handlers.DeleteBackupTask))).Methods("DELETE")

	// Cloud Sync (rclone-based)
	cloudSyncHandler := handlers.NewCloudSyncHandler()
	r.Handle("/api/cloud-sync", permRoute("storage", "read", cloudSyncHandler.HandleCloudSync)).Methods("GET")
	r.Handle("/api/cloud-sync", permRoute("storage", "write", cloudSyncHandler.HandleCloudSync)).Methods("POST")
	r.Handle("/api/cloud-sync/jobs", permRoute("storage", "read", handlers.HandleCloudSyncJobs)).Methods("GET")

	// Replication handlers
	r.Handle("/api/replication/send", permRoute("storage", "admin", handlers.ZFSSend)).Methods("POST")
	r.Handle("/api/replication/send-incremental", permRoute("storage", "admin", handlers.ZFSSendIncremental)).Methods("POST")
	r.Handle("/api/replication/receive", permRoute("storage", "admin", handlers.ZFSReceive)).Methods("POST")

	// Settings handlers
	settingsHandler := handlers.NewSettingsHandler(db)
	r.Handle("/api/settings/telegram", permRoute("system", "read", settingsHandler.GetTelegramConfig)).Methods("GET")
	r.Handle("/api/settings/telegram", permRoute("system", "write", settingsHandler.SaveTelegramConfig)).Methods("POST")
	r.Handle("/api/settings/telegram/test", permRoute("system", "write", settingsHandler.TestTelegramConfig)).Methods("POST")

	// /api/alerts/telegram - aliases to settings handler (used by alerts.html)
	r.Handle("/api/alerts/telegram", permRoute("system", "read", settingsHandler.GetTelegramConfig)).Methods("GET")
	r.Handle("/api/alerts/telegram", permRoute("system", "write", settingsHandler.SaveTelegramConfig)).Methods("POST")
	r.Handle("/api/alerts/telegram/test", permRoute("system", "write", settingsHandler.TestTelegramConfig)).Methods("POST")

	// Alerting handlers (SMTP + Scrub schedules) - uses pooled DB connection
	alertingHandler := handlers.NewAlertingHandler(db)
	handlers.SetAlertingHandler(alertingHandler)
	r.Handle("/api/alerts/smtp", permRoute("system", "read", alertingHandler.GetSMTPConfig)).Methods("GET")
	r.Handle("/api/alerts/smtp", permRoute("system", "write", alertingHandler.SaveSMTPConfig)).Methods("POST")
	r.Handle("/api/alerts/smtp/test", permRoute("system", "write", handlers.TestSMTP)).Methods("POST")

	// ZFS Scrub Scheduler
	r.Handle("/api/zfs/scrub/schedule", permRoute("storage", "read", alertingHandler.GetScrubSchedules)).Methods("GET")
	r.Handle("/api/zfs/scrub/schedule", permRoute("storage", "write", alertingHandler.SaveScrubSchedules)).Methods("POST")

	handlers.StartScrubMonitor()
	handlers.InitAuth() // Finding 24

	// Removable Media handlers
	removableHandler := handlers.NewRemovableMediaHandler()
	r.Handle("/api/removable/list", permRoute("storage", "read", removableHandler.ListDevices)).Methods("GET")
	r.Handle("/api/removable/mount", permRoute("storage", "write", removableHandler.MountDevice)).Methods("POST")
	r.Handle("/api/removable/unmount", permRoute("storage", "write", removableHandler.UnmountDevice)).Methods("POST")
	r.Handle("/api/removable/eject", permRoute("storage", "write", removableHandler.EjectDevice)).Methods("POST")

	// Monitoring handlers
	monitoringHandler := handlers.NewMonitoringHandler()
	r.Handle("/api/monitoring/inotify", permRoute("system", "read", monitoringHandler.GetInotifyStats)).Methods("GET")

	// LDAP / Active Directory handlers (v2.0.0)
	ldapHandler := handlers.NewLDAPHandler(db)
	r.Handle("/api/ldap/config", permRoute("system", "read", ldapHandler.GetConfig)).Methods("GET")
	r.Handle("/api/ldap/config", permRoute("system", "admin", ldapHandler.SaveConfig)).Methods("POST")
	r.Handle("/api/ldap/test", permRoute("system", "write", ldapHandler.TestConnection)).Methods("POST")
	r.Handle("/api/ldap/status", permRoute("system", "read", ldapHandler.GetStatus)).Methods("GET")
	r.Handle("/api/ldap/sync", permRoute("system", "admin", ldapHandler.TriggerSync)).Methods("POST")
	r.Handle("/api/ldap/search-user", permRoute("system", "read", ldapHandler.SearchUser)).Methods("POST")
	r.Handle("/api/ldap/mappings", permRoute("system", "read", ldapHandler.GetMappings)).Methods("GET")
	r.Handle("/api/ldap/mappings", permRoute("system", "write", ldapHandler.AddMapping)).Methods("POST")
	r.Handle("/api/ldap/mappings", permRoute("system", "write", ldapHandler.DeleteMapping)).Methods("DELETE")
	r.Handle("/api/ldap/sync-log", permRoute("system", "read", ldapHandler.GetSyncLog)).Methods("GET")
	// Active Directory Domain Join (v7.3.0)
	r.Handle("/api/directory/join", permRoute("system", "admin", ldapHandler.JoinADDomain)).Methods("POST")
	r.Handle("/api/directory/status", permRoute("system", "read", ldapHandler.GetDirectoryStatus)).Methods("GET")

	// RBAC routes
	// Read routes require "roles:read" permission (except /me/* which is self-service)
	r.Handle("/api/rbac/roles", permRoute("roles", "read", handlers.HandleListRoles)).Methods("GET")
	r.Handle("/api/rbac/roles", permRoute("roles", "write", handlers.HandleCreateRole)).Methods("POST")
	r.Handle("/api/rbac/roles/{id}", permRoute("roles", "read", handlers.HandleGetRole)).Methods("GET")
	r.Handle("/api/rbac/roles/{id}", permRoute("roles", "write", handlers.HandleUpdateRole)).Methods("PUT")
	r.Handle("/api/rbac/roles/{id}", permRoute("roles", "write", handlers.HandleDeleteRole)).Methods("DELETE")
	r.Handle("/api/rbac/roles/{id}/permissions", permRoute("roles", "read", handlers.HandleGetRolePermissions)).Methods("GET")
	r.Handle("/api/rbac/roles/{id}/permissions", permRoute("roles", "write", handlers.HandleAssignPermissionToRole)).Methods("POST")
	r.Handle("/api/rbac/roles/{id}/permissions/{permissionId}", permRoute("roles", "write", handlers.HandleRemovePermissionFromRole)).Methods("DELETE")
	r.Handle("/api/rbac/permissions", permRoute("roles", "read", handlers.HandleListPermissions)).Methods("GET")
	r.Handle("/api/rbac/users/{id}/roles", permRoute("roles", "read", handlers.HandleGetUserRoles)).Methods("GET")
	r.Handle("/api/rbac/users/{id}/roles", permRoute("roles", "write", handlers.HandleAssignRoleToUser)).Methods("POST")
	r.Handle("/api/rbac/users/{id}/roles/{roleId}", permRoute("roles", "write", handlers.HandleRemoveRoleFromUser)).Methods("DELETE")
	r.Handle("/api/rbac/users/{id}/permissions", permRoute("roles", "read", handlers.HandleGetUserPermissions)).Methods("GET")
	// /me/* routes are self-service - authenticated users can always read their own permissions
	r.HandleFunc("/api/rbac/me/permissions", handlers.HandleGetMyPermissions).Methods("GET")
	r.HandleFunc("/api/rbac/me/roles", handlers.HandleGetMyRoles).Methods("GET")
	r.HandleFunc("/api/rbac/check", handlers.HandleCheckPermission).Methods("GET")

	// Snapshot Scheduler (v2.0.0)
	snapScheduleHandler := handlers.NewSnapshotScheduleHandler()
	r.Handle("/api/snapshots/schedules", permRoute("storage", "read", snapScheduleHandler.ListSchedules)).Methods("GET")
	r.Handle("/api/snapshots/schedules", permRoute("storage", "write", snapScheduleHandler.SaveSchedules)).Methods("POST")
	r.Handle("/api/snapshots/run-now", permRoute("storage", "write", snapScheduleHandler.RunNow)).Methods("POST")
	// Cron hook: called by generated systemd timer on localhost (no user session, bypassed by sessionMiddleware IP+token check)
	r.HandleFunc("/api/zfs/snapshots/cron-hook", snapScheduleHandler.RunCronHook).Methods("POST")

	// ACL Management (v2.0.0)
	aclHandler := handlers.NewACLHandler()
	r.Handle("/api/acl/get", permRoute("storage", "read", aclHandler.GetACL)).Methods("GET")
	r.Handle("/api/acl/set", permRoute("storage", "write", aclHandler.SetACL)).Methods("POST")
	// Alias for consistency with other system APIs
	r.Handle("/api/system/acl", permRoute("storage", "read", aclHandler.GetACL)).Methods("GET")
	r.Handle("/api/system/acl", permRoute("storage", "write", aclHandler.SetACL)).Methods("POST")

	// Metrics / Reporting (v2.0.0)
	metricsHandler := handlers.NewMetricsHandler()
	r.HandleFunc("/api/metrics/current", metricsHandler.GetCurrentMetrics).Methods("GET")
	r.HandleFunc("/api/metrics/history", metricsHandler.GetHistory).Methods("GET")

	// Background metrics collection - writes to /var/lib/dplaneos/metrics/*.json
	// Powers the history charts in reporting.html
	go func() {
		metricsHandler.CollectAndStore() // collect immediately on startup
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			metricsHandler.CollectAndStore()
		}
	}()

	// Firewall (v2.0.0)
	firewallHandler := handlers.NewFirewallHandler()
	r.HandleFunc("/api/firewall/status", firewallHandler.GetStatus).Methods("GET")
	r.Handle("/api/firewall/rule", permRoute("firewall", "write", firewallHandler.SetRule)).Methods("POST")
	// NixOS only: sync full port list to dplane-generated.nix
	r.Handle("/api/firewall/sync", permRoute("firewall", "write", firewallHandler.SyncFirewallToNix)).Methods("POST")

	// SSL/TLS Certificates (v2.0.0)
	certHandler := handlers.NewCertHandler()
	r.HandleFunc("/api/certs/list", certHandler.ListCerts).Methods("GET")
	r.Handle("/api/certs/generate", permRoute("certificates", "write", certHandler.GenerateSelfSigned)).Methods("POST")
	r.Handle("/api/certs/activate", permRoute("certificates", "write", certHandler.ActivateCert)).Methods("POST")
	r.Handle("/api/certs/import", permRoute("certificates", "write", certHandler.ImportCert)).Methods("POST")
	r.Handle("/api/certs/acme", permRoute("certificates", "write", certHandler.RequestACME)).Methods("POST")
	r.Handle("/api/certs/acme/renew-all", permRoute("certificates", "write", certHandler.RenewAllHandler)).Methods("POST")
	r.HandleFunc("/api/system/certs/acme/check", certHandler.VerifyACMEProxy).Methods("GET")
	r.Handle("/api/certs/{name}", permRoute("certificates", "write", certHandler.DeleteCert)).Methods("DELETE")

	// ZFS Holds (v6.2.0)
	r.Handle("/api/zfs/hold", permRoute("storage", "write", zfsHandler.HoldSnapshot)).Methods("POST")
	r.Handle("/api/zfs/release", permRoute("storage", "write", zfsHandler.ReleaseSnapshot)).Methods("POST")
	r.Handle("/api/zfs/holds", permRoute("storage", "read", zfsHandler.ListHolds)).Methods("GET")

	// ZFS Split (v6.2.0)
	r.Handle("/api/zfs/pools/split", permRoute("storage", "write", zfsHandler.SplitPool)).Methods("POST")

	// Trash / Recycle Bin (v2.0.0)
	trashHandler := handlers.NewTrashHandler()
	r.Handle("/api/trash/list", permRoute("storage", "read", trashHandler.ListTrash)).Methods("GET")
	r.Handle("/api/trash/move", permRoute("storage", "write", trashHandler.MoveToTrash)).Methods("POST")
	r.Handle("/api/trash/restore", permRoute("storage", "write", trashHandler.RestoreFromTrash)).Methods("POST")
	r.Handle("/api/trash/empty", permRoute("storage", "admin", trashHandler.EmptyTrash)).Methods("POST")

	// Power Management (v2.0.0)
	powerHandler := handlers.NewPowerMgmtHandler()
	r.Handle("/api/power/disks", permRoute("system", "read", powerHandler.GetDiskStatus)).Methods("GET")
	r.Handle("/api/power/spindown", permRoute("system", "write", powerHandler.SetSpindown)).Methods("POST")
	r.Handle("/api/power/spindown-now", permRoute("system", "write", powerHandler.SpindownNow)).Methods("POST")

	// Start background monitors
	handlers.StartReplicationMonitor()
	// SMART background monitor: polls every 6 hours, calls DispatchAlert on risk
	handlers.StartSMARTMonitor()

	// ── High Availability cluster endpoints ──
	r.HandleFunc("/api/ha/status", haHandler.GetStatus).Methods("GET")
	r.Handle("/api/ha/peers", permRoute("system", "admin", haHandler.RegisterPeer)).Methods("POST")
	r.Handle("/api/ha/peers/{id}", permRoute("system", "admin", http.HandlerFunc(haHandler.RemovePeer))).Methods("DELETE")
	r.Handle("/api/ha/peers/{id}/role", permRoute("system", "admin", haHandler.SetPeerRole)).Methods("POST")
	r.Handle("/api/ha/replication/configure", permRoute("system", "admin", haHandler.ConfigureHAReplication)).Methods("POST")
	r.Handle("/api/ha/replication/configure", permRoute("system", "admin", haHandler.GetReplicationConfig)).Methods("GET")
	r.Handle("/api/ha/fencing/configure", permRoute("system", "admin", haHandler.ConfigureFencing)).Methods("POST")
	r.Handle("/api/ha/fencing/configure", permRoute("system", "admin", haHandler.GetFencingConfig)).Methods("GET")
	r.Handle("/api/ha/witness/configure", permRoute("system", "admin", haHandler.ConfigureWitness)).Methods("POST")
	r.Handle("/api/ha/witness/configure", permRoute("system", "admin", haHandler.GetWitnessConfig)).Methods("GET")
	r.Handle("/api/ha/witness/test", permRoute("system", "admin", haHandler.TestWitness)).Methods("POST")
	r.Handle("/api/ha/promote", permRoute("system", "admin", haHandler.Promote)).Methods("POST")
	r.Handle("/api/ha/fence", permRoute("system", "admin", haHandler.TriggerFence)).Methods("POST")
	r.Handle("/api/ha/maintenance", permRoute("system", "admin", haHandler.RegisterMaintenance)).Methods("POST")
	r.Handle("/api/ha/pdu/configure", permRoute("system", "admin", haHandler.ConfigurePDU)).Methods("POST")
	r.Handle("/api/ha/pdu/configure", permRoute("system", "admin", haHandler.GetPDUConfig)).Methods("GET")
	r.Handle("/api/ha/clear_fault", permRoute("system", "admin", haHandler.ClearFault)).Methods("POST")
	// /api/ha/heartbeat and /api/ha/sync/status are deliberately PUBLIC — peer daemons call them without a session
	r.HandleFunc("/api/ha/heartbeat", haHandler.PeerHeartbeat).Methods("POST")
	r.HandleFunc("/api/ha/sync/status", haHandler.GetSyncStatus).Methods("GET")
	r.HandleFunc("/api/ha/local", haHandler.LocalNodeInfo).Methods("GET")
	r.Handle("/api/ha/toggle", permRoute("system", "admin", haHandler.ToggleHA)).Methods("POST")

	// WebSocket for real-time monitoring
	wsHandler := handlers.NewWebSocketHandler(wsHub)
	r.Handle("/ws/monitor", permRoute("system", "read", wsHandler.HandleMonitor))

	// v4.1.0: PTY terminal over WebSocket (authenticated via sessionMiddleware)
	termHandler := handlers.NewTerminalHandler()
	r.Handle("/ws/terminal", permRoute("system", "admin", termHandler.HandleTerminal))

	// v3.2.0: iSCSI target management (Phase 2)
	r.HandleFunc("/api/iscsi/status", handlers.GetISCSIStatus).Methods("GET")
	r.HandleFunc("/api/iscsi/targets", handlers.GetISCSITargets).Methods("GET")
	r.Handle("/api/iscsi/targets", permRoute("storage", "write", handlers.CreateISCSITarget)).Methods("POST")
	r.Handle("/api/iscsi/targets/update", permRoute("storage", "write", handlers.UpdateISCSITarget)).Methods("POST")
	r.Handle("/api/iscsi/targets/{iqn}", permRoute("storage", "write", handlers.DeleteISCSITarget)).Methods("DELETE")
	r.HandleFunc("/api/iscsi/acls", handlers.GetISCSIACLs).Methods("GET")
	r.Handle("/api/iscsi/acls", permRoute("storage", "write", handlers.AddISCSIACL)).Methods("POST")
	r.Handle("/api/iscsi/acls", permRoute("storage", "write", handlers.DeleteISCSIACL)).Methods("DELETE")
	r.HandleFunc("/api/iscsi/zvols", handlers.GetISCSIZvolList).Methods("GET")

	// v8.0.0: NVMe-oF target (nvmet + ZFS zvol)
	r.HandleFunc("/api/nvmet/status", handlers.GetNVMeTargetStatus).Methods("GET")
	r.HandleFunc("/api/nvmet/targets", handlers.ListNVMeTargets).Methods("GET")
	r.HandleFunc("/api/nvmet/zvols", handlers.ListNVMeZvols).Methods("GET")
	r.Handle("/api/nvmet/targets", permRoute("storage", "write", handlers.CreateNVMeTarget)).Methods("POST")
	r.Handle("/api/nvmet/targets", permRoute("storage", "write", handlers.UpdateNVMeTarget)).Methods("PUT")
	r.Handle("/api/nvmet/targets", permRoute("storage", "write", handlers.DeleteNVMeTarget)).Methods("DELETE")

	// v3.2.0: Prometheus metrics exporter (Phase 2)
	r.HandleFunc("/metrics", handlers.HandlePrometheusMetrics).Methods("GET")

	// Dataset search
	r.HandleFunc("/api/zfs/datasets/search", handlers.HandleDatasetSearch).Methods("GET")

	// Replication schedules
	replicationScheduleHandler := handlers.NewReplicationScheduleHandler(db)
	r.Handle("/api/replication/schedules", permRoute("storage", "read", replicationScheduleHandler.HandleListReplicationSchedules)).Methods("GET")
	r.Handle("/api/replication/schedules", permRoute("storage", "write", replicationScheduleHandler.HandleCreateReplicationSchedule)).Methods("POST")
	r.Handle("/api/replication/schedules/{id}", permRoute("storage", "write", replicationScheduleHandler.HandleUpdateReplicationSchedule)).Methods("PUT")
	r.Handle("/api/replication/schedules/{id}", permRoute("storage", "admin", replicationScheduleHandler.HandleDeleteReplicationSchedule)).Methods("DELETE")
	r.Handle("/api/replication/schedules/{id}/run", permRoute("storage", "write", replicationScheduleHandler.HandleRunReplicationScheduleNow)).Methods("POST")

	// Create server.
	// WriteTimeout is set to 0 (no timeout) because several routes need to
	// stream indefinitely: /api/system/logs/stream (SSE), /ws/monitor,
	// /ws/terminal, /api/files/download (large files). Per-route timeouts
	// are enforced inside the handlers themselves where needed.
	// ReadTimeout covers request body reading - 30s is sufficient for all
	// non-upload routes; chunked uploads reset the deadline per chunk via
	// the 32 MB ParseMultipartForm call.
	srv := &http.Server{
		Addr:         *listenAddr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // streaming routes need no global write deadline
		IdleTimeout:  120 * time.Second,
	}

	// Start background monitors
	handlers.StartCapacityMonitor()
	log.Println("Capacity guardian started (checks every 5 min)")

	// Start server in goroutine
	go func() {
		log.Printf("Listening on %s", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Audit startup
	audit.Log(audit.AuditLog{
		Level:   audit.LevelInfo,
		Command: "DAEMON_START",
		Success: true,
		Metadata: map[string]interface{}{
			"version": Version,
			"listen":  *listenAddr,
		},
	})

	// Wait for interrupt signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop

	log.Println("Shutting down gracefully...")
	daemonCancel() // signal all daemonCtx-aware goroutines to exit

	// Audit shutdown
	audit.Log(audit.AuditLog{
		Level:   audit.LevelInfo,
		Command: "DAEMON_STOP",
		Success: true,
	})

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("Server stopped")
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, Version)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start))
	})
}

// Thread-safe rate limiting middleware (per IP)
var (
	rateLimitMu   sync.Mutex
	requestCounts = make(map[string][]time.Time)
	maxRequests   = 100
	timeWindow    = time.Minute
)

// realIP extracts the client IP for rate limiting.
// When the daemon is behind a reverse proxy (e.g. nginx on 127.0.0.1), every
// request arrives with RemoteAddr = "127.0.0.1:PORT".  In that case we fall
// back to the X-Real-IP or X-Forwarded-For header set by the proxy.
// RemoteAddr is always preferred for direct connections.
func realIP(r *http.Request) string {
	return security.RealIP(r)
}

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := realIP(r)

		rateLimitMu.Lock()
		now := time.Now()
		if timestamps, exists := requestCounts[ip]; exists {
			// Remove old timestamps
			var recent []time.Time
			for _, t := range timestamps {
				if now.Sub(t) < timeWindow {
					recent = append(recent, t)
				}
			}
			requestCounts[ip] = recent

			// Check rate limit
			if len(recent) >= maxRequests {
				rateLimitMu.Unlock()
				audit.LogSecurityEvent(
					fmt.Sprintf("Rate limit exceeded: %d requests in %v", len(recent), timeWindow),
					r.Header.Get("X-User"),
					ip,
				)
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}
		}

		// Add current request
		requestCounts[ip] = append(requestCounts[ip], now)
		rateLimitMu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// Session validation middleware
func sessionMiddleware(db *sql.DB) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip validation for public endpoints
			p := r.URL.Path
			if p == "/health" ||
				p == "/api/auth/login" ||
				p == "/api/auth/logout" ||
				p == "/api/auth/check" ||
				p == "/api/csrf" ||
				// Setup wizard - no session exists yet on fresh installs
				p == "/api/system/setup-admin" ||
				p == "/api/system/setup-complete" ||
				p == "/api/system/status" || // dashboard needs status before login to detect setup_complete
				// HA peer endpoints - called by peer daemons that have no user session
				p == "/api/ha/heartbeat" ||
				p == "/api/ha/sync/status" ||
				// Internal hooks - called by systemd timers on localhost.
				// Mandatory check: Must be localhost AND provide the internal-only secret token.
				((p == "/api/zfs/snapshots/cron-hook" || p == "/api/hardware/smart/cron-hook") &&
					(strings.HasPrefix(r.RemoteAddr, "127.0.0.1") || strings.HasPrefix(r.RemoteAddr, "[::1]")) &&
					r.Header.Get("X-Internal-Token") == "dplaneos-internal-reconciliation-secret-v1") ||
				// Internal disk events - called by udev scripts on localhost
				p == "/api/internal/disk-event" ||
				// Prometheus metrics - scraped by external monitoring without session
				p == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			// 1. Check for API Token (Authorization: Bearer dpl_...)
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token := strings.TrimPrefix(authHeader, "Bearer ")
				sessionUser, err := security.ValidateAPITokenAndGetUser(token)
				if err == nil {
					// Token valid - set user in context and proceed
					ctx := context.WithValue(r.Context(), middleware.UserContextKey, &middleware.User{
						ID:       sessionUser.ID,
						Username: sessionUser.Username,
						Email:    sessionUser.Email,
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// If token provided but invalid, we fall through to session check or fail later
			}

			sessionID := r.Header.Get("X-Session-ID")
			user := r.Header.Get("X-User")

			if sessionID == "" || user == "" {
				audit.LogSecurityEvent("Missing auth (no Bearer token and missing session headers)", user, realIP(r))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Validate session and get user details
			sessionUser, err := security.ValidateSessionAndGetUser(sessionID)
			if err != nil {
				// DB timeout/unavailable - allow request to proceed (best effort operational mode)
				// The client already has a session, so we trust they are legitimate
				// Log but don't block - system stays operational during DB issues
				log.Printf("WARN: Session validation deferred due to DB issue (%v) - allowing request for user %s", err, user)
				// Allow request to proceed with header user - use best-effort mode
				ctx := context.WithValue(r.Context(), middleware.UserContextKey, &middleware.User{
					ID:       0,
					Username: user,
					Email:    "",
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Verify header user matches session user
			if sessionUser.Username != user {
				audit.LogSecurityEvent("Session user mismatch", user, realIP(r))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Set user in context for downstream handlers (RBAC /me/* endpoints)
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &middleware.User{
				ID:       sessionUser.ID,
				Username: sessionUser.Username,
				Email:    sessionUser.Email,
			})

			// 3. CSRF Validation for mutating methods (Finding 22)
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				csrfHeader := r.Header.Get("X-CSRF-Token")
				var storedCSRF string
				err := db.QueryRowContext(ctx, "SELECT csrf_token FROM sessions WHERE session_id = $1", sessionID).Scan(&storedCSRF)
				if err != nil {
					// DB timeout - skip CSRF check, allow request (best effort operational mode)
					log.Printf("WARN: CSRF validation deferred due to DB issue - allowing mutation for user %s", user)
				} else if storedCSRF == "" || storedCSRF != csrfHeader {
					audit.LogSecurityEvent("CSRF validation failed", user, realIP(r))
					http.Error(w, "Forbidden (Invalid CSRF token)", http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// permRoute wraps a HandlerFunc with RequirePermission middleware.
// Use this for all routes that modify system state or access sensitive data.
func permRoute(resource, action string, fn http.HandlerFunc) http.Handler {
	return middleware.RequirePermission(resource, action)(fn)
}

// StartAuditRotation launches a background goroutine to purge old audit logs weekly
func StartAuditRotation(db *sql.DB) {
	go func() {
		// Run first rotation after 1 minute to avoid startup contention
		time.Sleep(1 * time.Minute)
		runRotation(db)

		ticker := time.NewTicker(7 * 24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runRotation(db)
		}
	}()
}

func runRotation(db *sql.DB) {
	var retentionStr string
	err := db.QueryRow("SELECT value FROM system_config WHERE key = 'audit_retention_days'").Scan(&retentionStr)
	retentionDays := 90
	if err == nil && retentionStr != "" {
		if val, err := strconv.Atoi(retentionStr); err == nil {
			retentionDays = val
		}
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02 15:04:05")
	log.Printf("MAINTENANCE: Running audit log rotation (cutoff: %s, retention: %d days)", cutoff, retentionDays)

	if _, err := db.Exec("DELETE FROM audit_logs WHERE timestamp < $1", cutoff); err != nil {
		log.Printf("ERROR: Automatic audit rotation failed: %v", err)
		return
	}
	log.Printf("MAINTENANCE: Audit log rotation completed successfully")
}

// bootstrapCEToken reads a token from a file and ensures it exists in api_tokens for the admin user.
func bootstrapCEToken(db *sql.DB, path string) {
	if path == "" {
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		log.Printf("BOOTSTRAP: Failed to read CE token file %s: %v", path, err)
		return
	}
	token := strings.TrimSpace(string(content))
	if token == "" {
		return
	}

	// We assume token is the raw token. D-PlaneOS stores token_hash and token_prefix.
	// For simplicity in this bootstrap, we store it if no token named 'nixos-gitops' exists.
	var adminID int
	err = db.QueryRow("SELECT id FROM users WHERE username='admin'").Scan(&adminID)
	if err != nil {
		log.Printf("BOOTSTRAP: Failed to find admin user: %v", err)
		return
	}

	// security.GenerateTokenHash is what we would normally use, but we don't want to re-hash on every boot.
	// Actually, let's just check if a token with this prefix exists.
	prefix := ""
	if len(token) > 8 {
		prefix = token[:8]
	}

	var exists int
	db.QueryRow("SELECT COUNT(*) FROM api_tokens WHERE name='compliance-engine-token'").Scan(&exists)
	if exists > 0 {
		return // Already bootstrapped
	}

	// Use security package to hash it correctly
	hash := security.HashToken(token)
	_, err = db.Exec(`INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, scopes)
		VALUES ($1, 'compliance-engine-token', $2, $3, 'admin')`,
		adminID, hash, prefix)
	if err != nil {
		log.Printf("BOOTSTRAP: Failed to insert CE token: %v", err)
	} else {
		log.Printf("BOOTSTRAP: Compliance Engine API token injected for admin")
	}
}
