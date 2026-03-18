package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
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
	"dplaned/internal/reconciler"
	"dplaned/internal/security"
	"dplaned/internal/websocket"
	"dplaned/internal/zfs"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

var (
	Version = "dev" // overridden at build time via: -ldflags "-X main.Version=$(cat VERSION)"
)

func main() {
	// Parse flags
	listenAddr := flag.String("listen", "127.0.0.1:9000", "Listen address")
	dbPath := flag.String("db", "/var/lib/dplaneos/dplaneos.db", "Path to SQLite database")
	telegramBot := flag.String("telegram-bot", "", "Telegram bot token (optional, for alerts)")
	telegramChat := flag.String("telegram-chat", "", "Telegram chat ID (optional, for alerts)")
	backupPath := flag.String("backup-path", "", "External path for DB backup (e.g., /mnt/usb/dplaneos-backup.db). If empty, backs up next to main DB.")
	configDir := flag.String("config-dir", "/etc/dplaneos", "Config directory (for NixOS: /var/lib/dplaneos/config)")
	smbConfPath := flag.String("smb-conf", "/etc/samba/smb.conf", "Path to write SMB config (for NixOS: /var/lib/dplaneos/smb-shares.conf)")
	haLocalID := flag.String("ha-local-id", "", "Unique ID for this cluster node (default: /etc/machine-id prefix)")
	haLocalAddr := flag.String("ha-local-addr", "", "HTTP address peers use to reach this daemon, e.g. http://10.0.0.1:5050")
	gitopsStatePath := flag.String("gitops-state", "/var/lib/dplaneos/gitops/state.yaml", "Path to GitOps state.yaml (managed by git repo)")
	applyOnly := flag.Bool("apply", false, "Apply GitOps state and exit (Phase 3.1)")
	testSerialization := flag.Bool("test-serialization", false, "Verify state.yaml round-trip (Phase 4.1)")
	testIdempotency := flag.Bool("test-idempotency", false, "Verify Apply(S); Apply(S) results in zero diff (Phase 4.2)")
	flag.Parse()

	// Phase 3.1: One-off apply if requested
	if *applyOnly {
		log.Printf("GITOPS: Running one-off apply from %s", *gitopsStatePath)
		// 1. Open DB
		db, err := sql.Open("sqlite3", *dbPath)
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
			log.Fatalf("Apply failed: %v (Status: %s, Reason: %s)", err, result.Status, result.HaltReason)
		}
		log.Printf("GITOPS: Apply complete! Applied: %v", result.Applied)
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
		// Deep compare (placeholder for now, or just check specific counts)
		if len(s1.Pools) != len(s2.Pools) || len(s1.Datasets) != len(s2.Datasets) {
			log.Fatalf("Round-trip mismatch in counts!")
		}
		log.Printf("COMPLIANCE: Serialization test PASSED")
		os.Exit(0)
	}

	if *testIdempotency {
		log.Printf("COMPLIANCE: Testing idempotency of %s", *gitopsStatePath)
		db, err := sql.Open("sqlite3", *dbPath)
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
	// Critical for systems with high I/O:
	// - WAL mode: concurrent reads during writes
	// - busy_timeout: wait 30s during WAL checkpoints (prevents "database locked")
	// - cache_size: 64MB in-memory cache
	// - wal_autocheckpoint: checkpoint every 1000 pages (~4MB) to prevent WAL bloat
	db, err := sql.Open("sqlite3", *dbPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_cache_size=-65536&_wal_autocheckpoint=1000&_synchronous=FULL")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Force WAL checkpoint on startup to clean any leftover WAL from crashes
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("Warning: initial WAL checkpoint failed: %v", err)
	}

	// Initialize database schema (IF NOT EXISTS - safe on every startup)
	if err := initSchema(db); err != nil {
		log.Fatalf("Database schema initialization failed: %v", err)
	}

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

	// Boot reconciler: fallback for systems where networkd was not active when
	// files were written, or for Debian/Ubuntu with NetworkManager instead of networkd.
	// On NixOS + networkd: this is a no-op (networkd already read the files at boot).
	go reconciler.Run(db)

	// Periodic WAL checkpoint every 5 minutes - safety net against WAL bloat
	// on systems with high audit logging rates (e.g., runaway container producing errors)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
				log.Printf("Warning: periodic WAL checkpoint failed: %v", err)
			}
		}
	}()

	// Daily VACUUM INTO - creates a clean backup copy of the database
	// Protects metadata against WAL corruption from hard power loss
	// Use -backup-path for off-pool backup (USB, second disk, NFS mount)
	go func() {
		dbBackupDest := *backupPath
		if dbBackupDest == "" {
			dbBackupDest = *dbPath + ".backup"
		}

		// Backup immediately on startup
		if _, err := db.Exec("VACUUM INTO ?", dbBackupDest); err != nil {
			log.Printf("Warning: startup DB backup failed: %v", err)
		} else {
			log.Printf("Startup DB backup created: %s", dbBackupDest)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := db.Exec("VACUUM INTO ?", dbBackupDest); err != nil {
				log.Printf("Warning: daily DB backup failed: %v", err)
			} else {
				log.Printf("Daily DB backup created: %s", dbBackupDest)
			}
		}
	}()

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

	// Initialize database connection for session validation
	if err := security.InitDatabase(*dbPath); err != nil {
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

	// ── Wire central alert dispatchers ─────────────────────────────────────
	// Must be called after DB is open (needed by SendWebhookAlert) and after
	// Telegram is initialized (alerts.SendAlert).
	// All subsystems (heartbeat, SMART monitor, capacity guardian, scrub monitor)
	// call handlers.DispatchAlert() which routes to these three functions.
	handlers.SetAlertDispatchers(
		// Webhook: send to all enabled, subscribed webhook configs
		func(event, resource, message string) {
			handlers.SendWebhookAlert(db, event, "critical", message,
				map[string]interface{}{"resource": resource})
		},
		// SMTP: uses the package-level sender which reads config from DB
		handlers.SendSMTPAlert,
		// Telegram: forward to the alerts package
		func(message string) {
			_ = alerts.SendAlert(alerts.TelegramAlert{
				Level:   "CRITICAL",
				Title:   "D-PlaneOS Alert",
				Message: message,
				Details: nil,
			})
		},
	)

	// Wire webhook + SMTP senders into the ZFS heartbeat package so that
	// pool CRITICAL / DEGRADED events also reach webhook and SMTP channels.
	zfs.SetAlertSenders(
		func(event, pool, msg string) {
			handlers.SendWebhookAlert(db, event, "critical", msg,
				map[string]interface{}{"pool": pool})
		},
		handlers.SendSMTPAlert,
	)

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

	log.Printf("D-PlaneOS Daemon v%s starting...", Version)

	// Initialize WebSocket Hub for real-time monitoring
	wsHub := websocket.NewMonitorHub()
	go wsHub.Run()

	// Wire the WS hub into disk-event handlers so they can broadcast
	// diskAdded / diskRemoved / poolHealthChanged events.
	handlers.SetDiskEventHub(wsHub)

	// Initialize ZED Event Listener (Unix Socket)
	go zfs.StartZEDListener("/run/dplaneos/dplaneos.sock",
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

	// Start job reaper: remove finished jobs after 1 hour
	jobs.StartReaper(1 * time.Hour)

	// Start background audit log rotation
	StartAuditRotation(db)

	// Create router
	r := mux.NewRouter()

	// Middleware
	r.Use(loggingMiddleware)
	r.Use(sessionMiddleware)
	r.Use(rateLimitMiddleware)

	// Health check

	// ─── AUTH ROUTES (public, no session required) ───
	authHandler := handlers.NewAuthHandler(db)
	r.HandleFunc("/api/auth/login", authHandler.Login).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/auth/logout", authHandler.Logout).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/auth/check", authHandler.Check).Methods("GET")
	r.HandleFunc("/api/auth/session", authHandler.Session).Methods("GET")
	r.HandleFunc("/api/auth/change-password", authHandler.ChangePassword).Methods("POST")
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
	r.HandleFunc("/api/zfs/command", zfsHandler.HandleCommand).Methods("POST")
	r.HandleFunc("/api/zfs/pools", zfsHandler.ListPools).Methods("GET")
	r.HandleFunc("/api/zfs/datasets", zfsHandler.ListDatasets).Methods("GET")
	r.HandleFunc("/api/zfs/datasets", zfsHandler.CreateDataset).Methods("POST")

	// ZFS Encryption handlers
	zfsEncryptionHandler := handlers.NewZFSEncryptionHandler()
	r.HandleFunc("/api/zfs/encryption/list", zfsEncryptionHandler.ListEncryptedDatasets).Methods("GET")
	r.HandleFunc("/api/zfs/encryption/unlock", zfsEncryptionHandler.UnlockDataset).Methods("POST")
	r.HandleFunc("/api/zfs/encryption/lock", zfsEncryptionHandler.LockDataset).Methods("POST")
	r.HandleFunc("/api/zfs/encryption/create", zfsEncryptionHandler.CreateEncryptedDataset).Methods("POST")
	r.HandleFunc("/api/zfs/encryption/change-key", zfsEncryptionHandler.ChangeKey).Methods("POST")

	// System handlers
	systemHandler := handlers.NewSystemHandler()
	r.HandleFunc("/api/system/health", handlers.SystemHealthHandler).Methods("GET")
	r.HandleFunc("/api/system/logs/stream", handlers.LogStreamHandler).Methods("GET")
	r.HandleFunc("/api/system/ups", systemHandler.GetUPSStatus).Methods("GET")
	r.HandleFunc("/api/system/ups", systemHandler.SaveUPSConfig).Methods("POST")
	r.HandleFunc("/api/system/network", systemHandler.GetNetworkInfo).Methods("GET")
	r.HandleFunc("/api/system/logs", systemHandler.GetSystemLogs).Methods("GET")

	// Docker handlers
	dockerHandler := handlers.NewDockerHandler()
	r.HandleFunc("/api/docker/containers", dockerHandler.ListContainers).Methods("GET")
	r.HandleFunc("/api/docker/icon-map", handlers.HandleDockerIconMap).Methods("GET")
	// Custom icon assets - served from /var/lib/dplaneos/custom_icons/
	// ORDER MATTERS: the exact /list route must be registered before the PathPrefix
	// catch-all, because gorilla/mux matches in registration order and PathPrefix
	// would otherwise intercept /api/assets/custom-icons/list.
	r.HandleFunc("/api/assets/custom-icons/list", handlers.HandleCustomIconList).Methods("GET")
	r.PathPrefix("/api/assets/custom-icons/").HandlerFunc(handlers.HandleCustomIconFile).Methods("GET")
	r.Handle("/api/docker/action", permRoute("docker", "write", dockerHandler.ContainerAction)).Methods("POST")
	r.HandleFunc("/api/docker/logs", dockerHandler.ContainerLogs).Methods("GET")
	// v3.0.0: Docker enhanced
	r.HandleFunc("/api/docker/update", dockerHandler.SafeUpdate).Methods("POST")
	r.HandleFunc("/api/docker/pull", dockerHandler.PullImage).Methods("POST")
	r.HandleFunc("/api/docker/remove", dockerHandler.RemoveContainer).Methods("POST")
	r.HandleFunc("/api/docker/prune", dockerHandler.PruneDocker).Methods("POST")
	r.HandleFunc("/api/docker/stats", dockerHandler.ContainerStats).Methods("GET")
	r.HandleFunc("/api/docker/compose/up", dockerHandler.ComposeUp).Methods("POST")
	r.HandleFunc("/api/docker/compose/down", dockerHandler.ComposeDown).Methods("POST")
	r.HandleFunc("/api/docker/compose/status", dockerHandler.ComposeStatus).Methods("GET")

	// v3.0.0: ZFS Snapshots CRUD
	snapshotCRUDHandler := handlers.NewZFSSnapshotHandler()
	r.HandleFunc("/api/zfs/snapshots", snapshotCRUDHandler.ListSnapshots).Methods("GET")
	r.HandleFunc("/api/zfs/snapshots", snapshotCRUDHandler.CreateSnapshot).Methods("POST")
	r.HandleFunc("/api/zfs/snapshots", snapshotCRUDHandler.DestroySnapshot).Methods("DELETE")
	r.HandleFunc("/api/zfs/snapshots/rollback", snapshotCRUDHandler.RollbackSnapshot).Methods("POST")

	// v3.0.0: ZFS Replication (remote send/recv)
	replicationRemoteHandler := handlers.NewReplicationHandler()
	r.HandleFunc("/api/replication/remote", replicationRemoteHandler.ReplicateToRemote).Methods("POST")
	r.HandleFunc("/api/replication/test", replicationRemoteHandler.TestRemoteConnection).Methods("POST")
	r.HandleFunc("/api/replication/ssh-keygen", handlers.GenerateReplicationKey).Methods("POST")
	r.HandleFunc("/api/replication/ssh-pubkey", handlers.GetReplicationPubKey).Methods("GET")
	r.HandleFunc("/api/replication/ssh-copy-id", handlers.CopyReplicationKey).Methods("POST")

	// v3.0.0: ZFS Time Machine (browse snapshots, restore single files)
	timeMachineHandler := handlers.NewZFSTimeMachineHandler()
	r.HandleFunc("/api/timemachine/versions", timeMachineHandler.ListSnapshotVersions).Methods("GET")
	r.HandleFunc("/api/timemachine/browse", timeMachineHandler.BrowseSnapshot).Methods("GET")
	r.HandleFunc("/api/timemachine/restore", timeMachineHandler.RestoreFile).Methods("POST")

	// v3.0.0: ZFS Sandbox (ephemeral Docker environments via ZFS clone)
	sandboxHandler := handlers.NewZFSSandboxHandler()
	r.HandleFunc("/api/sandbox/create", sandboxHandler.CreateSandbox).Methods("POST")
	r.HandleFunc("/api/sandbox/list", sandboxHandler.ListSandboxes).Methods("GET")
	r.HandleFunc("/api/sandbox/destroy", sandboxHandler.DestroySandbox).Methods("DELETE", "POST")

	// v3.0.0: NixOS Config Guard (only active on NixOS systems)
	nixosGuardHandler := handlers.NewNixOSGuardHandler(db)
	r.HandleFunc("/api/nixos/detect", nixosGuardHandler.DetectNixOS).Methods("GET")
	r.HandleFunc("/api/nixos/validate", nixosGuardHandler.ValidateConfig).Methods("POST")
	r.HandleFunc("/api/nixos/generations", nixosGuardHandler.ListGenerations).Methods("GET")
	r.Handle("/api/nixos/rollback", permRoute("system", "admin", nixosGuardHandler.RollbackGeneration)).Methods("POST")

	// v3.0.0: ZFS Health Predictor (deep monitoring, heatmap data)
	healthHandler := handlers.NewZFSHealthHandler()
	r.HandleFunc("/api/zfs/health", healthHandler.GetPoolHealth).Methods("GET")
	r.HandleFunc("/api/zfs/iostat", healthHandler.GetIOStats).Methods("GET")
	r.HandleFunc("/api/zfs/events", healthHandler.GetPoolEvents).Methods("GET")
	r.HandleFunc("/api/zfs/smart", healthHandler.GetSMARTHealth).Methods("GET")

	// v3.0.0: Pool Capacity Guardian (prevents ZFS full freeze)
	capacityHandler := handlers.NewCapacityGuardianHandler()
	r.HandleFunc("/api/zfs/capacity", capacityHandler.GetCapacityStatus).Methods("GET")
	r.HandleFunc("/api/zfs/capacity/reserve", capacityHandler.SetupReserve).Methods("POST")
	r.HandleFunc("/api/zfs/capacity/release", capacityHandler.ReleaseReserve).Methods("POST")

	// v3.0.0: Power-loss state locks
	stateLockHandler := handlers.NewStateLockHandler()
	r.HandleFunc("/api/system/stale-locks", stateLockHandler.CheckStaleLocks).Methods("GET")
	r.HandleFunc("/api/system/stale-locks/clear", stateLockHandler.ClearStaleLock).Methods("POST")

	// v3.0.0: Sandbox orphan cleanup
	r.HandleFunc("/api/sandbox/cleanup", sandboxHandler.CleanOrphanVolumes).Methods("POST")

	// v3.0.0: NixOS diff + watchdog
	r.HandleFunc("/api/nixos/diff", nixosGuardHandler.DiffGenerations).Methods("GET")
	r.Handle("/api/nixos/apply", permRoute("system", "admin", nixosGuardHandler.ApplyWithWatchdog)).Methods("POST")
	r.HandleFunc("/api/nixos/confirm", nixosGuardHandler.ConfirmApply).Methods("POST")
	r.HandleFunc("/api/nixos/watchdog", nixosGuardHandler.WatchdogStatus).Methods("GET")
	r.HandleFunc("/api/nixos/pre-upgrade-snapshots", nixosGuardHandler.ListPreUpgradeSnapshots).Methods("GET")

	// v3.0.0: Docker pre-flight check
	r.HandleFunc("/api/docker/preflight", dockerHandler.PreFlightCheck).Methods("GET")

	// ── Git Sync ──
	gitSyncHandler := handlers.NewGitSyncHandler(db)
	r.HandleFunc("/api/git-sync/config", gitSyncHandler.GetConfig).Methods("GET")
	r.HandleFunc("/api/git-sync/config", gitSyncHandler.SaveConfig).Methods("POST")
	r.HandleFunc("/api/git-sync/pull", gitSyncHandler.Pull).Methods("POST")
	r.HandleFunc("/api/git-sync/status", gitSyncHandler.Status).Methods("GET")
	r.HandleFunc("/api/git-sync/stacks", gitSyncHandler.ListStacks).Methods("GET")
	r.HandleFunc("/api/git-sync/deploy", gitSyncHandler.Deploy).Methods("POST")
	r.HandleFunc("/api/git-sync/export", gitSyncHandler.ExportContainers).Methods("POST")
	r.HandleFunc("/api/git-sync/push", gitSyncHandler.Push).Methods("POST")

	// Git-Sync: Multi-Repo + Credentials (v2.1.1)
	gitReposHandler := handlers.NewGitReposHandler(db)
	r.HandleFunc("/api/git-sync/credentials", gitReposHandler.ListCredentials).Methods("GET")
	r.HandleFunc("/api/git-sync/credentials", gitReposHandler.SaveCredential).Methods("POST")
	r.HandleFunc("/api/git-sync/credentials/test", gitReposHandler.TestCredential).Methods("POST")
	r.HandleFunc("/api/git-sync/credentials/delete", gitReposHandler.DeleteCredential).Methods("DELETE", "POST")
	r.HandleFunc("/api/git-sync/repos", gitReposHandler.ListRepos).Methods("GET")
	r.HandleFunc("/api/git-sync/repos", gitReposHandler.SaveRepo).Methods("POST")
	r.HandleFunc("/api/git-sync/repos/delete", gitReposHandler.DeleteRepo).Methods("DELETE", "POST")
	r.HandleFunc("/api/git-sync/repos/pull", gitReposHandler.PullRepo).Methods("POST")
	r.HandleFunc("/api/git-sync/repos/push", gitReposHandler.PushRepo).Methods("POST")
	r.HandleFunc("/api/git-sync/repos/deploy", gitReposHandler.DeployRepo).Methods("POST")
	r.HandleFunc("/api/git-sync/repos/browse", gitReposHandler.BrowseFiles).Methods("GET")
	r.HandleFunc("/api/git-sync/credentials/branches", gitReposHandler.ListBranches).Methods("GET")
	r.HandleFunc("/api/git-sync/repos/export", gitReposHandler.ExportToRepo).Methods("POST")
	gitSyncHandler.StartAutoSync()

	// v5.1: Compose stack management
	stackHandler := handlers.NewStackHandler(db)
	r.HandleFunc("/api/docker/stacks", stackHandler.ListStacks).Methods("GET")
	r.Handle("/api/docker/stacks/deploy", permRoute("docker", "write", stackHandler.DeployStack)).Methods("POST")
	r.HandleFunc("/api/docker/stacks/yaml", stackHandler.GetStackYAML).Methods("GET")
	r.Handle("/api/docker/stacks/yaml", permRoute("docker", "write", stackHandler.UpdateStackYAML)).Methods("PUT")
	r.Handle("/api/docker/stacks", permRoute("docker", "write", stackHandler.DeleteStack)).Methods("DELETE")
	r.Handle("/api/docker/stacks/action", permRoute("docker", "write", stackHandler.StackAction)).Methods("POST")
	r.Handle("/api/docker/convert-run", permRoute("docker", "write", stackHandler.ConvertDockerRun)).Methods("POST")

	// v5.1: Multi-stack templates
	templateHandler := handlers.NewTemplateHandler()
	r.HandleFunc("/api/docker/templates", templateHandler.ListTemplates).Methods("GET")
	r.HandleFunc("/api/docker/templates/installed", templateHandler.ListInstalledTemplates).Methods("GET")
	r.Handle("/api/docker/templates/deploy", permRoute("docker", "write", templateHandler.DeployTemplate)).Methods("POST")

	// v3.0.0: Audit log rotation
	auditRotationHandler := handlers.NewAuditRotationHandler()
	r.Handle("/api/system/audit/rotate", permRoute("system", "admin", auditRotationHandler.RotateAuditLogs)).Methods("POST")
	r.Handle("/api/system/audit/stats", permRoute("audit", "read", auditRotationHandler.GetAuditStats)).Methods("GET")
	r.Handle("/api/system/audit/verify-chain", permRoute("audit", "read", auditRotationHandler.VerifyAuditChain)).Methods("GET")

	supportBundleHandler := handlers.NewSupportBundleHandler(db, Version)
	r.Handle("/api/system/support-bundle", permRoute("system", "admin", supportBundleHandler.GenerateBundle)).Methods("POST")

	webhookHandler := handlers.NewWebhookHandler(db, Version)
	r.HandleFunc("/api/alerts/webhooks", webhookHandler.ListWebhooks).Methods("GET")
	r.Handle("/api/alerts/webhooks", permRoute("system", "write", webhookHandler.SaveWebhook)).Methods("POST")
	r.Handle("/api/alerts/webhooks/{id}", permRoute("system", "write", webhookHandler.DeleteWebhook)).Methods("DELETE")
	r.HandleFunc("/api/alerts/webhooks/{id}/test", webhookHandler.TestWebhook).Methods("POST")

	// Phase 3: GitOps - declarative state reconciliation
	gitopsHandler := handlers.NewGitOpsHandler(db, *gitopsStatePath, *smbConfPath, wsHub)
	defer gitopsHandler.Stop()

	// Start GitOps drift detector - polls every 5 minutes and broadcasts
	// "gitops.drift" WS events so GitOpsPage reacts in real time.
	driftDetector := gitops.NewDriftDetector(db, *gitopsStatePath, 5*time.Minute, wsHub)
	driftDetector.Start()
	defer driftDetector.Stop()
	r.HandleFunc("/api/gitops/status", gitopsHandler.Status).Methods("GET")
	r.HandleFunc("/api/gitops/plan", gitopsHandler.Plan).Methods("GET")
	r.Handle("/api/gitops/apply", permRoute("system", "admin", gitopsHandler.Apply)).Methods("POST")
	r.Handle("/api/gitops/approve", permRoute("system", "admin", gitopsHandler.Approve)).Methods("POST")
	r.HandleFunc("/api/gitops/check", gitopsHandler.Check).Methods("POST")
	r.HandleFunc("/api/gitops/state", gitopsHandler.GetState).Methods("GET")
	r.Handle("/api/gitops/state", permRoute("system", "admin", gitopsHandler.PutState)).Methods("PUT")
	r.HandleFunc("/api/gitops/settings", gitopsHandler.GetSettings).Methods("GET")
	r.Handle("/api/gitops/settings", permRoute("system", "admin", gitopsHandler.UpdateSettings)).Methods("PUT")
	r.Handle("/api/gitops/sync", permRoute("system", "admin", gitopsHandler.SyncNow)).Methods("POST")

	// v3.0.0: Zombie disk watcher
	zombieHandler := handlers.NewZombieWatcherHandler()
	r.HandleFunc("/api/zfs/disk-latency", zombieHandler.CheckDiskLatency).Methods("GET")

	// v3.0.0: LDAP Circuit Breaker
	r.HandleFunc("/api/ldap/circuit-breaker", handlers.GetCircuitBreakerStatus).Methods("GET")
	r.HandleFunc("/api/ldap/circuit-breaker/reset", handlers.ResetCircuitBreaker).Methods("POST")

	// v3.0.0: ZFS Scrub management
	r.HandleFunc("/api/zfs/scrub/start", handlers.StartScrub).Methods("POST")
	r.HandleFunc("/api/zfs/scrub/stop", handlers.StopScrub).Methods("POST")
	r.HandleFunc("/api/zfs/scrub/status", handlers.GetScrubStatus).Methods("GET")

	// Pool maintenance operations
	r.Handle("/api/zfs/pool/operations", permRoute("storage", "write", handlers.PoolOperations)).Methods("POST")

	// Resilver progress (separate from scrub - parses resilver-specific scan lines)
	r.HandleFunc("/api/zfs/resilver/status", handlers.HandleResilverStatus).Methods("GET")

	// v3.0.0: VDEV / Pool expansion
	r.Handle("/api/zfs/pool/add-vdev", permRoute("storage", "write", handlers.AddVdevToPool)).Methods("POST")
	r.HandleFunc("/api/zfs/pool/remove-device", handlers.RemoveCacheOrLog).Methods("POST")
	r.Handle("/api/zfs/pool/replace", permRoute("storage", "write", handlers.ReplaceDisk)).Methods("POST")

	// v3.0.0: Dataset quotas
	r.HandleFunc("/api/zfs/dataset/quota", handlers.SetDatasetQuota).Methods("POST")
	r.HandleFunc("/api/zfs/dataset/quota", handlers.GetDatasetQuota).Methods("GET")

	// v3.0.0: Per-user and per-group quotas (ZFS userquota/groupquota)
	r.HandleFunc("/api/zfs/quota/usergroup", handlers.GetUserGroupQuotas).Methods("GET")
	r.HandleFunc("/api/zfs/quota/usergroup", handlers.SetUserGroupQuota).Methods("POST")

	// v3.0.0: S.M.A.R.T. tests
	r.HandleFunc("/api/zfs/smart/test", handlers.RunSMARTTest).Methods("POST")
	r.HandleFunc("/api/zfs/smart/results", handlers.GetSMARTTestResults).Methods("GET")
	// SMART failure prediction (calls PredictDiskFailure via TranslateSMARTAttribute)
	r.HandleFunc("/api/zfs/smart/predict", handlers.HandleSMARTPrediction).Methods("GET")

	// v3.0.0: ZFS delegation (zfs allow)
	r.HandleFunc("/api/zfs/delegation", handlers.SetZFSDelegation).Methods("POST")
	r.HandleFunc("/api/zfs/delegation", handlers.GetZFSDelegation).Methods("GET")
	r.HandleFunc("/api/zfs/delegation/revoke", handlers.RevokeZFSDelegation).Methods("POST")

	// v3.0.0: Network rollback
	r.Handle("/api/network/apply", permRoute("network", "write", handlers.ApplyNetworkWithRollback)).Methods("POST")
	r.HandleFunc("/api/network/confirm", handlers.ConfirmNetwork).Methods("POST")

	// v3.0.0: SMB VFS modules
	r.HandleFunc("/api/smb/vfs", handlers.GetSMBVFSConfig).Methods("GET")
	r.HandleFunc("/api/smb/vfs", handlers.SetSMBVFSConfig).Methods("POST")

	// v3.0.0: VLAN management
	r.HandleFunc("/api/network/vlan", handlers.ListVLANs).Methods("GET")
	r.HandleFunc("/api/network/vlan", handlers.CreateVLAN).Methods("POST")
	r.HandleFunc("/api/network/vlan", handlers.DeleteVLAN).Methods("DELETE")

	// v3.0.0: Link Aggregation / Bonding
	r.HandleFunc("/api/network/bond", handlers.ListBonds).Methods("GET")
	r.Handle("/api/network/bond", permRoute("network", "write", handlers.CreateBond)).Methods("POST")
	r.Handle("/api/network/bond/{name}", permRoute("network", "write", handlers.DeleteBond)).Methods("DELETE")

	// v3.0.0: NTP configuration
	r.HandleFunc("/api/system/ntp", handlers.GetNTPStatus).Methods("GET")
	r.HandleFunc("/api/system/ntp", handlers.SetNTPServers).Methods("POST")

	// Shares handlers (config management)
	r.HandleFunc("/api/shares/smb/reload", handlers.ReloadSMBConfig).Methods("POST")
	r.HandleFunc("/api/shares/smb/test", handlers.TestSMBConfig).Methods("POST")
	r.HandleFunc("/api/shares/nfs/reload", handlers.ReloadNFSExports).Methods("POST")
	r.HandleFunc("/api/shares/nfs/list", handlers.ListNFSExports).Methods("GET")

	// NFS CRUD handler - NFSHandler manages /etc/exports via SQLite
	nfsHandler := handlers.NewNFSHandler(db)
	r.HandleFunc("/api/nfs/status", nfsHandler.GetNFSStatus).Methods("GET")
	r.HandleFunc("/api/nfs/exports", nfsHandler.ListNFSExports).Methods("GET")
	r.HandleFunc("/api/nfs/exports", nfsHandler.CreateNFSExport).Methods("POST")
	r.HandleFunc("/api/nfs/exports/{id}/update", nfsHandler.UpdateNFSExport).Methods("POST")
	r.HandleFunc("/api/nfs/exports/{id}", nfsHandler.DeleteNFSExport).Methods("DELETE")
	r.HandleFunc("/api/nfs/reload", nfsHandler.ReloadNFSExportsHandler).Methods("POST")

	// Shares CRUD handlers
	shareCRUDHandler := handlers.NewShareCRUDHandler(db, *smbConfPath)
	r.HandleFunc("/api/shares/list", shareCRUDHandler.HandleShares).Methods("GET")
	r.HandleFunc("/api/shares", shareCRUDHandler.HandleShares).Methods("GET")
	r.Handle("/api/shares", permRoute("shares", "write", shareCRUDHandler.HandleShares)).Methods("POST")
	r.Handle("/api/shares", permRoute("shares", "write", shareCRUDHandler.HandleShares)).Methods("DELETE")

	// User & Group CRUD handlers
	userGroupHandler := handlers.NewUserGroupHandler(db)
	r.HandleFunc("/api/rbac/users", userGroupHandler.HandleUsers).Methods("GET")
	r.Handle("/api/rbac/users", permRoute("users", "write", userGroupHandler.HandleUsers)).Methods("POST")
	r.HandleFunc("/api/rbac/groups", userGroupHandler.HandleGroups).Methods("GET", "POST")
	r.HandleFunc("/api/users/create", userGroupHandler.HandleUsers).Methods("POST")

	// System status, profile, preflight, setup handlers
	systemStatusHandler := handlers.NewSystemStatusHandler(db, Version)
	r.HandleFunc("/api/system/setup-admin", systemStatusHandler.HandleSetupAdmin).Methods("POST")
	r.HandleFunc("/api/system/status", systemStatusHandler.HandleStatus).Methods("GET")
	r.HandleFunc("/api/system/profile", systemStatusHandler.HandleProfile).Methods("GET")
	r.HandleFunc("/api/system/settings", systemStatusHandler.HandleSettings).Methods("GET", "POST")
	r.HandleFunc("/api/system/preflight", systemStatusHandler.HandlePreflight).Methods("GET")

	// OTA update endpoints (Debian/Ubuntu)
	r.HandleFunc("/api/system/updates/check", handlers.HandleUpdatesCheck).Methods("GET")
	r.Handle("/api/system/updates/apply", permRoute("system", "admin", handlers.HandleUpdatesApply)).Methods("POST")
	r.Handle("/api/system/updates/apply-security", permRoute("system", "admin", handlers.HandleUpdatesApplySecurity)).Methods("POST")
	r.HandleFunc("/api/system/updates/daemon-version", handlers.HandleDaemonVersion).Methods("GET")
	r.HandleFunc("/api/system/zfs-gate-status", systemStatusHandler.HandleZFSGateStatus).Methods("GET")
	// v3.0.0: IPMI/BMC sensor data (graceful no-op if ipmitool unavailable)
	r.HandleFunc("/api/system/ipmi", systemStatusHandler.HandleIPMISensors).Methods("GET")
	// /api/status is an alias for /api/system/status (used by dashboard ECC check)
	r.HandleFunc("/api/status", systemStatusHandler.HandleStatus).Methods("GET")
	r.HandleFunc("/api/system/setup-complete", systemStatusHandler.HandleSetupComplete).Methods("POST")
	r.HandleFunc("/api/system/metrics", handlers.HandleSystemMetrics).Methods("GET")
	r.HandleFunc("/api/system/tuning", handlers.HandleSystemSettings).Methods("GET", "POST")

	// Disk discovery (setup wizard)
	r.HandleFunc("/api/system/disks", handlers.HandleDiskDiscovery).Methods("GET")
	r.Handle("/api/system/pool/create", permRoute("storage", "write", handlers.HandlePoolCreate)).Methods("POST")

	// Disk lifecycle event endpoint (localhost only - called by udev/systemd)
	r.HandleFunc("/api/internal/disk-event", handlers.HandleDiskEvent).Methods("POST")

	// Files handlers
	filesHandler := handlers.NewFilesExtendedHandler()
	r.HandleFunc("/api/files/list", filesHandler.ListFiles).Methods("GET")
	r.HandleFunc("/api/files/properties", filesHandler.GetFileProperties).Methods("GET")
	r.HandleFunc("/api/files/read", filesHandler.ReadFile).Methods("GET")
	r.HandleFunc("/api/files/download", filesHandler.DownloadFile).Methods("GET")
	r.HandleFunc("/api/files/rename", filesHandler.RenameFile).Methods("POST")
	r.HandleFunc("/api/files/copy", filesHandler.CopyFile).Methods("POST")
	r.HandleFunc("/api/files/move", filesHandler.MoveFile).Methods("POST")
	r.HandleFunc("/api/files/write", filesHandler.WriteFile).Methods("POST")
	r.HandleFunc("/api/files/upload", filesHandler.UploadChunk).Methods("POST")
	r.HandleFunc("/api/files/mkdir", handlers.CreateDirectory).Methods("POST")
	r.HandleFunc("/api/files/delete", handlers.DeletePath).Methods("POST")
	r.HandleFunc("/api/files/chown", handlers.ChangeOwnership).Methods("POST")
	r.HandleFunc("/api/files/chmod", handlers.ChangePermissions).Methods("POST")

	// Backup handlers
	r.HandleFunc("/api/backup/rsync", handlers.ExecuteRsync).Methods("GET", "POST")
	r.Handle("/api/backup/rsync/{id}", permRoute("storage", "write", http.HandlerFunc(handlers.DeleteBackupTask))).Methods("DELETE")

	// Cloud Sync (rclone-based)
	cloudSyncHandler := handlers.NewCloudSyncHandler()
	r.HandleFunc("/api/cloud-sync", cloudSyncHandler.HandleCloudSync).Methods("GET", "POST")
	r.HandleFunc("/api/cloud-sync/jobs", handlers.HandleCloudSyncJobs).Methods("GET")

	// Replication handlers
	r.HandleFunc("/api/replication/send", handlers.ZFSSend).Methods("POST")
	r.HandleFunc("/api/replication/send-incremental", handlers.ZFSSendIncremental).Methods("POST")
	r.HandleFunc("/api/replication/receive", handlers.ZFSReceive).Methods("POST")

	// Settings handlers
	settingsHandler := handlers.NewSettingsHandler(db)
	r.HandleFunc("/api/settings/telegram", settingsHandler.GetTelegramConfig).Methods("GET")
	r.Handle("/api/settings/telegram", permRoute("system", "write", settingsHandler.SaveTelegramConfig)).Methods("POST")
	r.HandleFunc("/api/settings/telegram/test", settingsHandler.TestTelegramConfig).Methods("POST")

	// /api/alerts/telegram - aliases to settings handler (used by alerts.html)
	r.HandleFunc("/api/alerts/telegram", settingsHandler.GetTelegramConfig).Methods("GET")
	r.Handle("/api/alerts/telegram", permRoute("system", "write", settingsHandler.SaveTelegramConfig)).Methods("POST")
	r.HandleFunc("/api/alerts/telegram/test", settingsHandler.TestTelegramConfig).Methods("POST")

	// Alerting handlers (SMTP + Scrub schedules) - uses pooled DB connection
	alertingHandler := handlers.NewAlertingHandler(db)
	handlers.SetAlertingHandler(alertingHandler)
	r.HandleFunc("/api/alerts/smtp", alertingHandler.GetSMTPConfig).Methods("GET")
	r.Handle("/api/alerts/smtp", permRoute("system", "write", alertingHandler.SaveSMTPConfig)).Methods("POST")
	r.HandleFunc("/api/alerts/smtp/test", handlers.TestSMTP).Methods("POST")

	// ZFS Scrub Scheduler
	r.HandleFunc("/api/zfs/scrub/schedule", alertingHandler.GetScrubSchedules).Methods("GET")
	r.HandleFunc("/api/zfs/scrub/schedule", alertingHandler.SaveScrubSchedules).Methods("POST")

	handlers.StartScrubMonitor()

	// Removable Media handlers
	removableHandler := handlers.NewRemovableMediaHandler()
	r.HandleFunc("/api/removable/list", removableHandler.ListDevices).Methods("GET")
	r.HandleFunc("/api/removable/mount", removableHandler.MountDevice).Methods("POST")
	r.HandleFunc("/api/removable/unmount", removableHandler.UnmountDevice).Methods("POST")
	r.HandleFunc("/api/removable/eject", removableHandler.EjectDevice).Methods("POST")

	// Monitoring handlers
	monitoringHandler := handlers.NewMonitoringHandler()
	r.HandleFunc("/api/monitoring/inotify", monitoringHandler.GetInotifyStats).Methods("GET")

	// LDAP / Active Directory handlers (v2.0.0)
	ldapHandler := handlers.NewLDAPHandler(db)
	r.HandleFunc("/api/ldap/config", ldapHandler.GetConfig).Methods("GET")
	r.Handle("/api/ldap/config", permRoute("system", "admin", ldapHandler.SaveConfig)).Methods("POST")
	r.HandleFunc("/api/ldap/test", ldapHandler.TestConnection).Methods("POST")
	r.HandleFunc("/api/ldap/status", ldapHandler.GetStatus).Methods("GET")
	r.HandleFunc("/api/ldap/sync", ldapHandler.TriggerSync).Methods("POST")
	r.HandleFunc("/api/ldap/search-user", ldapHandler.SearchUser).Methods("POST")
	r.HandleFunc("/api/ldap/mappings", ldapHandler.GetMappings).Methods("GET")
	r.HandleFunc("/api/ldap/mappings", ldapHandler.AddMapping).Methods("POST")
	r.HandleFunc("/api/ldap/mappings", ldapHandler.DeleteMapping).Methods("DELETE")
	r.HandleFunc("/api/ldap/sync-log", ldapHandler.GetSyncLog).Methods("GET")

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
	r.HandleFunc("/api/snapshots/schedules", snapScheduleHandler.ListSchedules).Methods("GET")
	r.HandleFunc("/api/snapshots/schedules", snapScheduleHandler.SaveSchedules).Methods("POST")
	r.HandleFunc("/api/snapshots/run-now", snapScheduleHandler.RunNow).Methods("POST")
	// Cron hook: called by generated crontab instead of raw zfs snapshot
	// Handles snapshot creation, retention pruning, and post-snapshot replication trigger
	r.HandleFunc("/api/zfs/snapshots/cron-hook", snapScheduleHandler.RunCronHook).Methods("POST")

	// ACL Management (v2.0.0)
	aclHandler := handlers.NewACLHandler()
	r.HandleFunc("/api/acl/get", aclHandler.GetACL).Methods("GET")
	r.HandleFunc("/api/acl/set", aclHandler.SetACL).Methods("POST")
	// Alias for consistency with other system APIs
	r.HandleFunc("/api/system/acl", aclHandler.GetACL).Methods("GET")
	r.HandleFunc("/api/system/acl", aclHandler.SetACL).Methods("POST")

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

	// Trash / Recycle Bin (v2.0.0)
	trashHandler := handlers.NewTrashHandler()
	r.HandleFunc("/api/trash/list", trashHandler.ListTrash).Methods("GET")
	r.HandleFunc("/api/trash/move", trashHandler.MoveToTrash).Methods("POST")
	r.HandleFunc("/api/trash/restore", trashHandler.RestoreFromTrash).Methods("POST")
	r.HandleFunc("/api/trash/empty", trashHandler.EmptyTrash).Methods("POST")

	// Power Management (v2.0.0)
	powerHandler := handlers.NewPowerMgmtHandler()
	r.HandleFunc("/api/power/disks", powerHandler.GetDiskStatus).Methods("GET")
	r.HandleFunc("/api/power/spindown", powerHandler.SetSpindown).Methods("POST")
	r.HandleFunc("/api/power/spindown-now", powerHandler.SpindownNow).Methods("POST")

	// Start background monitors
	handlers.StartReplicationMonitor()
	// SMART background monitor: polls every 6 hours, calls DispatchAlert on risk
	handlers.StartSMARTMonitor()

	// ── High Availability cluster endpoints ──
	r.HandleFunc("/api/ha/status", haHandler.GetStatus).Methods("GET")
	r.Handle("/api/ha/peers", permRoute("system", "admin", haHandler.RegisterPeer)).Methods("POST")
	r.Handle("/api/ha/peers/{id}", permRoute("system", "admin", http.HandlerFunc(haHandler.RemovePeer))).Methods("DELETE")
	r.Handle("/api/ha/peers/{id}/role", permRoute("system", "admin", haHandler.SetPeerRole)).Methods("POST")
	// /api/ha/heartbeat is deliberately PUBLIC (no session) so peer daemons can reach it
	r.HandleFunc("/api/ha/heartbeat", haHandler.PeerHeartbeat).Methods("POST")
	r.HandleFunc("/api/ha/local", haHandler.LocalNodeInfo).Methods("GET")

	// WebSocket for real-time monitoring
	wsHandler := handlers.NewWebSocketHandler(wsHub)
	r.HandleFunc("/ws/monitor", wsHandler.HandleMonitor)

	// v4.1.0: PTY terminal over WebSocket (authenticated via sessionMiddleware)
	termHandler := handlers.NewTerminalHandler()
	r.HandleFunc("/ws/terminal", termHandler.HandleTerminal)

	// v3.2.0: iSCSI target management (Phase 2)
	r.HandleFunc("/api/iscsi/status", handlers.GetISCSIStatus).Methods("GET")
	r.HandleFunc("/api/iscsi/targets", handlers.GetISCSITargets).Methods("GET")
	r.Handle("/api/iscsi/targets", permRoute("storage", "write", handlers.CreateISCSITarget)).Methods("POST")
	r.Handle("/api/iscsi/targets/{iqn}", permRoute("storage", "write", handlers.DeleteISCSITarget)).Methods("DELETE")
	r.HandleFunc("/api/iscsi/acls", handlers.GetISCSIACLs).Methods("GET")
	r.Handle("/api/iscsi/acls", permRoute("storage", "write", handlers.AddISCSIACL)).Methods("POST")
	r.Handle("/api/iscsi/acls", permRoute("storage", "write", handlers.DeleteISCSIACL)).Methods("DELETE")
	r.HandleFunc("/api/iscsi/zvols", handlers.GetISCSIZvolList).Methods("GET")

	// v3.2.0: Prometheus metrics exporter (Phase 2)
	r.HandleFunc("/metrics", handlers.HandlePrometheusMetrics).Methods("GET")

	// Dataset search
	r.HandleFunc("/api/zfs/datasets/search", handlers.HandleDatasetSearch).Methods("GET")

	// Replication schedules
	replicationScheduleHandler := handlers.NewReplicationScheduleHandler(db)
	r.HandleFunc("/api/replication/schedules", replicationScheduleHandler.HandleListReplicationSchedules).Methods("GET")
	r.HandleFunc("/api/replication/schedules", replicationScheduleHandler.HandleCreateReplicationSchedule).Methods("POST")
	r.HandleFunc("/api/replication/schedules/{id}", replicationScheduleHandler.HandleDeleteReplicationSchedule).Methods("DELETE")
	r.HandleFunc("/api/replication/schedules/{id}/run", replicationScheduleHandler.HandleRunReplicationScheduleNow).Methods("POST")

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
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsLoopback() {
		return host // direct connection - trust RemoteAddr
	}
	// Behind a proxy - trust forwarded headers (proxy is on loopback, so not
	// externally injectable).
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// X-Forwarded-For may be "client, proxy1, proxy2" - take the first entry.
		if idx := strings.Index(v, ","); idx != -1 {
			return strings.TrimSpace(v[:idx])
		}
		return strings.TrimSpace(v)
	}
	return host
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
func sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip validation for public endpoints
		p := r.URL.Path
		if p == "/health" ||
			strings.HasPrefix(p, "/api/auth/") ||
			p == "/api/csrf" ||
			// Setup wizard - no session exists yet on fresh installs
			p == "/api/system/setup-admin" ||
			p == "/api/system/setup-complete" ||
			p == "/api/system/status" || // dashboard needs status before login to detect setup_complete
			// HA heartbeat - called by peer daemons that have no user session
			p == "/api/ha/heartbeat" ||
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
			// DB error means we cannot verify the session - reject with 401
			// (Do NOT fall through without user context: downstream RBAC handlers
			//  depend on the context value being present and will panic/misbehave.)
			audit.LogSecurityEvent("Session validation DB error: "+err.Error(), user, realIP(r))
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

	if _, err := db.Exec("DELETE FROM audit_logs WHERE timestamp < ?", cutoff); err != nil {
		log.Printf("ERROR: Automatic audit rotation failed: %v", err)
		return
	}

	if _, err := db.Exec("VACUUM"); err != nil {
		log.Printf("ERROR: Post-rotation VACUUM failed: %v", err)
	} else {
		log.Printf("MAINTENANCE: Audit log rotation and VACUUM completed successfully")
	}
}

