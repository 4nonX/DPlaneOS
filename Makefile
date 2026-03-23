.PHONY: all build install test clean deps

BINARY_NAME=dplaned
BUILD_DIR=build
INSTALL_DIR=/opt/dplaneos
GO=go

all: build

deps:
	@echo "Resolving Go dependencies..."
	@if [ -d daemon/vendor ]; then \
		echo "Using existing vendor directory (offline mode)"; \
	else \
		cd daemon && $(GO) mod tidy; \
	fi
	@echo "Dependencies resolved."

VERSION=$(shell cat VERSION 2>/dev/null || echo "unknown")

build: build-amd64

build-amd64: deps
	@echo "Building D-PlaneOS Daemon (amd64)..."
	@mkdir -p $(BUILD_DIR)
	cd daemon && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/dplaned
	@cp $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(BUILD_DIR)/$(BINARY_NAME)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64"

build-arm64: deps
	@echo "Building D-PlaneOS Daemon (arm64)..."
	@mkdir -p $(BUILD_DIR)
	@if ! command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then \
		echo "ERROR: aarch64-linux-gnu-gcc not found. Install with: apt install gcc-aarch64-linux-gnu"; exit 1; \
	fi
	cd daemon && GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc $(GO) build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/dplaned
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64"

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
	sudo install -m 644 install/systemd/dplaned.service /etc/systemd/system/
	sudo cp -r app/* $(INSTALL_DIR)/app/ 2>/dev/null || true
	sudo cp install/scripts/*.sh $(INSTALL_DIR)/install/scripts/ 2>/dev/null && sudo chmod +x $(INSTALL_DIR)/install/scripts/*.sh || true
	# ZED hook for real-time ZFS event notification
	@if [ -d /etc/zfs/zed.d ]; then \
		sudo install -m 755 install/zed/dplaneos-notify.sh /etc/zfs/zed.d/ && \
		echo "ZED hook installed"; \
	else \
		echo "Warning: /etc/zfs/zed.d not found - ZED hook skipped (install ZFS first)"; \
	fi
	# udev rules for removable media detection
	sudo install -m 644 install/udev/99-dplaneos-removable-media.rules /etc/udev/rules.d/ 2>/dev/null && \
		sudo udevadm control --reload-rules 2>/dev/null && echo "udev rules installed" || true
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
	@echo "  Example:  ExecStart=... -backup-path /mnt/usb/backups"
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
	@echo "D-PlaneOS Build System"
	@echo ""
	@echo "Targets:"
	@echo "  deps         - Resolve Go dependencies (needs internet)"
	@echo "  build        - Build the daemon binary (PostgreSQL-only)"
	@echo "  install      - Build and install daemon + systemd service"
	@echo "  test         - Run tests"
	@echo "  clean        - Remove build artifacts"
	@echo "  start        - Start the daemon service"
	@echo "  stop         - Stop the daemon service"
	@echo "  restart      - Restart the daemon service"
	@echo "  status       - Check daemon status"
	@echo "  logs         - Follow daemon logs"
	@echo "  audit        - Follow audit logs"

