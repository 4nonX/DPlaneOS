.PHONY: all build install test clean deps

BINARY_NAME=dplaned
BUILD_DIR=build
INSTALL_DIR=/opt/dplaneos
GO=go

all: build

deps:
	@echo "Resolving Go dependencies..."
	cd daemon && $(GO) mod tidy
	@echo "Dependencies resolved."

build: deps
	@echo "Building D-PlaneOS Daemon..."
	@echo "Pre-flight checks..."
	@command -v go >/dev/null 2>&1 || { echo "ERROR: Go not found. Install with: apt install golang-go"; exit 1; }
	@command -v gcc >/dev/null 2>&1 || { echo "ERROR: gcc not found (required for CGO/SQLite). Install with: apt install build-essential"; exit 1; }
	@mkdir -p $(BUILD_DIR)
	cd daemon && CGO_ENABLED=1 $(GO) build -ldflags="-s -w" -o ../$(BUILD_DIR)/$(BINARY_NAME) ./cmd/dplaned
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

install:
	@if [ ! -f $(BUILD_DIR)/$(BINARY_NAME) ]; then \
		echo "No pre-built binary found, building..."; \
		$(MAKE) build; \
	else \
		echo "Using pre-built binary: $(BUILD_DIR)/$(BINARY_NAME)"; \
	fi
	@echo "Installing D-PlaneOS..."
	sudo mkdir -p $(INSTALL_DIR)/daemon
	sudo mkdir -p $(INSTALL_DIR)/scripts
	sudo mkdir -p $(INSTALL_DIR)/app
	sudo mkdir -p /var/log/dplaneos
	sudo mkdir -p /var/lib/dplaneos
	sudo mkdir -p /var/lib/dplaneos/notifications
	sudo mkdir -p /run/dplaneos
	sudo install -m 755 $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/daemon/
	sudo install -m 644 systemd/dplaned.service /etc/systemd/system/
	sudo cp -r app/* $(INSTALL_DIR)/app/ 2>/dev/null || true
	sudo cp scripts/*.sh $(INSTALL_DIR)/scripts/ 2>/dev/null && sudo chmod +x $(INSTALL_DIR)/scripts/*.sh || true
	# ZED hook for real-time ZFS event notification
	@if [ -d /etc/zfs/zed.d ]; then \
		sudo install -m 755 zed/dplaneos-notify.sh /etc/zfs/zed.d/ && \
		echo "✓ ZED hook installed"; \
	else \
		echo "⚠ /etc/zfs/zed.d not found — ZED hook skipped (install ZFS first)"; \
	fi
	# udev rules for removable media detection
	sudo install -m 644 udev/99-dplaneos-removable-media.rules /etc/udev/rules.d/ 2>/dev/null && \
		sudo udevadm control --reload-rules 2>/dev/null && echo "✓ udev rules installed" || true
	sudo systemctl daemon-reload
	@echo ""
	@echo "═══════════════════════════════════════"
	@echo "  D-PlaneOS installed successfully"
	@echo "═══════════════════════════════════════"
	@echo "  Daemon:   $(INSTALL_DIR)/daemon/$(BINARY_NAME)"
	@echo "  Web UI:   $(INSTALL_DIR)/app/"
	@echo "  Config:   /etc/dplaneos/"
	@echo "  Data:     /var/lib/dplaneos/"
	@echo "  Logs:     /var/log/dplaneos/"
	@echo ""
	@echo "  Start:    sudo systemctl start dplaned"
	@echo "  Status:   sudo systemctl status dplaned"
	@echo ""
	@echo "  Optional: Set -backup-path for off-pool DB backup"
	@echo "  Example:  ExecStart=... -backup-path /mnt/usb/dplaneos.db.backup"
	@echo "═══════════════════════════════════════"

test:
	cd daemon && $(GO) test -v ./...

clean:
	rm -rf $(BUILD_DIR)
	cd daemon && $(GO) clean

start:
	sudo systemctl start dplaned

stop:
	sudo systemctl stop dplaned

restart:
	sudo systemctl restart dplaned

status:
	sudo systemctl status dplaned

logs:
	sudo journalctl -u dplaned -f

audit:
	sudo tail -f /var/log/dplaneos/audit.log

help:
	@echo "D-PlaneOS v3.3.1 Build System"
	@echo ""
	@echo "Targets:"
	@echo "  deps         - Resolve Go dependencies (needs internet)"
	@echo "  build        - Build the daemon binary (CGO_ENABLED=1 for SQLite)"
	@echo "  install      - Build and install daemon + systemd service"
	@echo "  test         - Run tests"
	@echo "  clean        - Remove build artifacts"
	@echo "  start        - Start the daemon service"
	@echo "  stop         - Stop the daemon service"
	@echo "  restart      - Restart the daemon service"
	@echo "  status       - Check daemon status"
	@echo "  logs         - Follow daemon logs"
	@echo "  audit        - Follow audit logs"
