<?php
/**
 * D-PlaneOS v1.14.0 - Real-time Event Stream
 * Server-Sent Events (SSE) for hardware monitoring and system alerts
 */

// Set headers for SSE
header('Content-Type: text/event-stream');
header('Cache-Control: no-cache');
header('Connection: keep-alive');
header('X-Accel-Buffering: no'); // Disable nginx buffering

// Prevent timeout
set_time_limit(0);
ini_set('max_execution_time', 0);

require_once __DIR__ . '/hardware-monitor.php';

/**
 * Send SSE event to client
 */
function sendEvent($event, $data, $id = null) {
    if ($id) {
        echo "id: $id\n";
    }
    echo "event: $event\n";
    echo "data: " . json_encode($data) . "\n\n";
    
    // Flush output buffer
    if (ob_get_level() > 0) {
        ob_flush();
    }
    flush();
}

/**
 * Send heartbeat to keep connection alive
 */
function sendHeartbeat() {
    echo ": heartbeat\n\n";
    if (ob_get_level() > 0) {
        ob_flush();
    }
    flush();
}

// Initialize
$monitor = new HardwareMonitor();
$lastCheck = 0;
$eventId = 1;
$heartbeatInterval = 15; // seconds
$checkInterval = 2; // seconds - check for changes every 2 seconds
$lastHeartbeat = time();

// Send initial connection event
sendEvent('connected', [
    'message' => 'Hardware monitoring active',
    'timestamp' => time()
], $eventId++);

// Main event loop
while (true) {
    $now = time();
    
    // Send heartbeat to keep connection alive
    if (($now - $lastHeartbeat) >= $heartbeatInterval) {
        sendHeartbeat();
        $lastHeartbeat = $now;
    }
    
    // Check for hardware changes
    if (($now - $lastCheck) >= $checkInterval) {
        try {
            // Detect hardware changes
            $changes = $monitor->detectChanges();
            
            // Send disk added events
            foreach ($changes['added'] as $disk) {
                sendEvent('disk_added', [
                    'disk' => $disk,
                    'message' => "New disk detected: {$disk['model']} ({$disk['sizeFormatted']})",
                    'timestamp' => $now
                ], $eventId++);
            }
            
            // Send disk removed events
            foreach ($changes['removed'] as $disk) {
                sendEvent('disk_removed', [
                    'disk' => $disk,
                    'message' => "Disk removed: {$disk['model']}",
                    'timestamp' => $now,
                    'critical' => $disk['inUse'] // Critical if disk was in use
                ], $eventId++);
            }
            
            // Send disk changed events
            foreach ($changes['changed'] as $change) {
                $disk = $change['disk'];
                $changeDetails = $change['changes'];
                
                // SMART status changed
                if (isset($changeDetails['smart'])) {
                    $isCritical = $changeDetails['smart']['new'] === 'FAILED';
                    sendEvent('disk_smart_changed', [
                        'disk' => $disk,
                        'old_status' => $changeDetails['smart']['old'],
                        'new_status' => $changeDetails['smart']['new'],
                        'message' => "Disk SMART status changed: {$disk['model']} is now {$changeDetails['smart']['new']}",
                        'timestamp' => $now,
                        'critical' => $isCritical
                    ], $eventId++);
                }
                
                // Usage status changed
                if (isset($changeDetails['usage'])) {
                    $message = $changeDetails['usage']['new'] 
                        ? "Disk {$disk['model']} is now in use by pool {$disk['pool']}"
                        : "Disk {$disk['model']} is no longer in use";
                    
                    sendEvent('disk_usage_changed', [
                        'disk' => $disk,
                        'in_use' => $changeDetails['usage']['new'],
                        'pool' => $disk['pool'],
                        'message' => $message,
                        'timestamp' => $now
                    ], $eventId++);
                }
            }
            
            // Check pool health
            $poolHealth = $monitor->checkPoolHealth();
            foreach ($poolHealth as $pool) {
                if ($pool['critical']) {
                    sendEvent('pool_health_critical', [
                        'pool' => $pool['name'],
                        'health' => $pool['health'],
                        'message' => "CRITICAL: Pool '{$pool['name']}' is {$pool['health']}",
                        'timestamp' => $now,
                        'critical' => true
                    ], $eventId++);
                } elseif ($pool['degraded']) {
                    sendEvent('pool_health_warning', [
                        'pool' => $pool['name'],
                        'health' => $pool['health'],
                        'message' => "WARNING: Pool '{$pool['name']}' is {$pool['health']}",
                        'timestamp' => $now,
                        'warning' => true
                    ], $eventId++);
                }
            }
            
            // Check for recent ZFS events
            $zfsEvents = $monitor->getPoolEvents();
            // Filter only events from last check
            $recentEvents = array_filter($zfsEvents, function($event) use ($lastCheck) {
                return $event['time'] >= $lastCheck;
            });
            
            foreach ($recentEvents as $event) {
                $severity = 'info';
                $critical = false;
                
                // Determine severity based on event class
                if (stripos($event['class'], 'fault') !== false) {
                    $severity = 'critical';
                    $critical = true;
                } elseif (stripos($event['class'], 'error') !== false) {
                    $severity = 'error';
                } elseif (stripos($event['class'], 'warning') !== false) {
                    $severity = 'warning';
                }
                
                sendEvent('zfs_event', [
                    'event' => $event,
                    'severity' => $severity,
                    'message' => "ZFS Event: {$event['class']} on pool {$event['pool']}",
                    'timestamp' => $now,
                    'critical' => $critical
                ], $eventId++);
            }
            
        } catch (Exception $e) {
            sendEvent('error', [
                'message' => 'Monitoring error: ' . $e->getMessage(),
                'timestamp' => $now
            ], $eventId++);
        }
        
        $lastCheck = $now;
    }
    
    // Check if client disconnected
    if (connection_aborted()) {
        break;
    }
    
    // Sleep for 1 second
    sleep(1);
}
