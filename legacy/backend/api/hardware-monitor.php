<?php
/**
 * D-PlaneOS v1.14.0 - Hardware Monitor
 * Real-time disk detection, insertion, removal, and failure monitoring
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

class HardwareMonitor {
    private $diskPath = '/dev/disk/by-id';
    private $statePath = '/var/lib/dplaneos/hardware-state.json';
    
    public function __construct() {
        // Ensure state directory exists
        $dir = dirname($this->statePath);
        if (!is_dir($dir)) {
            mkdir($dir, 0755, true);
        }
    }
    
    /**
     * Scan all available disks
     */
    public function scanDisks() {
        $disks = [];
        
        if (!is_dir($this->diskPath)) {
            return ['error' => 'Disk path not accessible'];
        }
        
        $entries = scandir($this->diskPath);
        
        foreach ($entries as $entry) {
            if ($entry === '.' || $entry === '..') continue;
            
            // Skip partitions, only get main disks
            if (strpos($entry, '-part') !== false) continue;
            
            $path = $this->diskPath . '/' . $entry;
            
            // Resolve symlink to actual device
            $realPath = realpath($path);
            if (!$realPath) continue;
            
            // Get disk info
            $diskInfo = $this->getDiskInfo($entry, $realPath);
            if ($diskInfo) {
                $disks[] = $diskInfo;
            }
        }
        
        return $disks;
    }
    
    /**
     * Get detailed disk information
     */
    private function getDiskInfo($id, $device) {
        // Get size
        $size = $this->getDiskSize($device);
        
        // Get model/serial from ID
        $model = $this->parseModel($id);
        
        // Check if disk is in use by ZFS
        $inUse = $this->isDiskInUse($device);
        
        // Get SMART status
        $smart = $this->getSmartStatus($device);
        
        // Get temperature
        $temp = $this->getDiskTemp($device);
        
        return [
            'id' => $id,
            'device' => $device,
            'name' => basename($device),
            'size' => $size,
            'sizeFormatted' => $this->formatBytes($size),
            'model' => $model,
            'inUse' => $inUse,
            'pool' => $inUse ? $this->getPoolName($device) : null,
            'smart' => $smart,
            'temperature' => $temp,
            'path' => $this->diskPath . '/' . $id,
            'timestamp' => time()
        ];
    }
    
    /**
     * Get disk size in bytes
     */
    private function getDiskSize($device) {
        $output = shell_exec("blockdev --getsize64 $device 2>/dev/null");
        return $output ? (int)trim($output) : 0;
    }
    
    /**
     * Parse model from disk ID
     */
    private function parseModel($id) {
        // Examples:
        // ata-WDC_WD40EFRX-68N32N0_WD-WCC7K0123456
        // ata-Samsung_SSD_860_EVO_500GB_S3Z1NB0K123456B
        
        if (strpos($id, 'ata-') === 0) {
            $parts = explode('_', substr($id, 4));
            $model = implode(' ', array_slice($parts, 0, -1));
            return str_replace('-', ' ', $model);
        }
        
        return $id;
    }
    
    /**
     * Check if disk is being used by ZFS
     */
    private function isDiskInUse($device) {
        $output = shell_exec("zpool status 2>/dev/null | grep -q " . escapeshellarg(basename($device)) . " && echo 'yes' || echo 'no'");
        return trim($output) === 'yes';
    }
    
    /**
     * Get pool name if disk is in use
     */
    private function getPoolName($device) {
        $deviceName = basename($device);
        $output = shell_exec("zpool status -v 2>/dev/null | grep -B 20 " . escapeshellarg($deviceName) . " | grep 'pool:' | head -1 | awk '{print $2}'");
        return $output ? trim($output) : null;
    }
    
    /**
     * Get SMART status
     */
    private function getSmartStatus($device) {
        // Check if smartctl is available
        if (!file_exists('/usr/sbin/smartctl')) {
            return 'UNKNOWN';
        }
        
        $output = shell_exec("/usr/sbin/smartctl -H $device 2>/dev/null | grep 'SMART overall-health'");
        
        if (strpos($output, 'PASSED') !== false) {
            return 'PASSED';
        } elseif (strpos($output, 'FAILED') !== false) {
            return 'FAILED';
        }
        
        return 'UNKNOWN';
    }
    
    /**
     * Get disk temperature
     */
    private function getDiskTemp($device) {
        if (!file_exists('/usr/sbin/smartctl')) {
            return null;
        }
        
        $output = shell_exec("/usr/sbin/smartctl -A $device 2>/dev/null | grep Temperature_Celsius | awk '{print $10}'");
        return $output ? (int)trim($output) : null;
    }
    
    /**
     * Detect hardware changes since last scan
     */
    public function detectChanges() {
        $currentState = $this->scanDisks();
        $previousState = $this->loadState();
        
        $changes = [
            'added' => [],
            'removed' => [],
            'changed' => [],
            'timestamp' => time()
        ];
        
        // Get disk IDs
        $currentIds = array_column($currentState, 'id');
        $previousIds = array_column($previousState, 'id');
        
        // Detect added disks
        foreach ($currentState as $disk) {
            if (!in_array($disk['id'], $previousIds)) {
                $changes['added'][] = $disk;
            }
        }
        
        // Detect removed disks
        foreach ($previousState as $disk) {
            if (!in_array($disk['id'], $currentIds)) {
                $changes['removed'][] = $disk;
            }
        }
        
        // Detect status changes (SMART, temperature, pool assignment)
        foreach ($currentState as $currentDisk) {
            foreach ($previousState as $previousDisk) {
                if ($currentDisk['id'] === $previousDisk['id']) {
                    $hasChanges = false;
                    $changeDetails = [];
                    
                    if ($currentDisk['smart'] !== $previousDisk['smart']) {
                        $hasChanges = true;
                        $changeDetails['smart'] = [
                            'old' => $previousDisk['smart'],
                            'new' => $currentDisk['smart']
                        ];
                    }
                    
                    if ($currentDisk['inUse'] !== $previousDisk['inUse']) {
                        $hasChanges = true;
                        $changeDetails['usage'] = [
                            'old' => $previousDisk['inUse'],
                            'new' => $currentDisk['inUse']
                        ];
                    }
                    
                    if ($hasChanges) {
                        $changes['changed'][] = [
                            'disk' => $currentDisk,
                            'changes' => $changeDetails
                        ];
                    }
                }
            }
        }
        
        // Save current state
        $this->saveState($currentState);
        
        return $changes;
    }
    
    /**
     * Load previous hardware state
     */
    private function loadState() {
        if (file_exists($this->statePath)) {
            $json = file_get_contents($this->statePath);
            return json_decode($json, true) ?: [];
        }
        return [];
    }
    
    /**
     * Save current hardware state
     */
    private function saveState($state) {
        file_put_contents($this->statePath, json_encode($state, JSON_PRETTY_PRINT));
    }
    
    /**
     * Get ZFS pool events
     */
    public function getPoolEvents() {
        // Get recent ZFS events
        $output = shell_exec("zpool events -H 2>/dev/null | tail -n 50");
        
        if (!$output) {
            return [];
        }
        
        $events = [];
        $lines = explode("\n", trim($output));
        
        foreach ($lines as $line) {
            if (empty($line)) continue;
            
            $parts = preg_split('/\s+/', $line);
            
            if (count($parts) >= 4) {
                $events[] = [
                    'time' => $parts[0],
                    'class' => $parts[1],
                    'pool' => $parts[2] ?? null,
                    'event' => $parts[3] ?? null,
                    'raw' => $line
                ];
            }
        }
        
        return $events;
    }
    
    /**
     * Monitor pool health
     */
    public function checkPoolHealth() {
        $output = shell_exec("zpool list -H -o name,health 2>/dev/null");
        
        if (!$output) {
            return [];
        }
        
        $pools = [];
        $lines = explode("\n", trim($output));
        
        foreach ($lines as $line) {
            if (empty($line)) continue;
            
            $parts = preg_split('/\s+/', $line);
            $pools[] = [
                'name' => $parts[0],
                'health' => $parts[1],
                'degraded' => $parts[1] !== 'ONLINE',
                'critical' => in_array($parts[1], ['DEGRADED', 'FAULTED', 'UNAVAIL'])
            ];
        }
        
        return $pools;
    }
    
    /**
     * Format bytes to human readable
     */
    private function formatBytes($bytes) {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $i = 0;
        while ($bytes >= 1024 && $i < count($units) - 1) {
            $bytes /= 1024;
            $i++;
        }
        return round($bytes, 2) . ' ' . $units[$i];
    }
}

// ==========================================
// API ENDPOINTS
// ==========================================

$monitor = new HardwareMonitor();
$action = $_GET['action'] ?? 'scan';

switch ($action) {
    case 'scan':
        // Get all available disks
        echo json_encode([
            'disks' => $monitor->scanDisks(),
            'timestamp' => time()
        ]);
        break;
        
    case 'changes':
        // Detect hardware changes
        echo json_encode($monitor->detectChanges());
        break;
        
    case 'pool_events':
        // Get ZFS events
        echo json_encode([
            'events' => $monitor->getPoolEvents(),
            'timestamp' => time()
        ]);
        break;
        
    case 'pool_health':
        // Check pool health
        echo json_encode([
            'pools' => $monitor->checkPoolHealth(),
            'timestamp' => time()
        ]);
        break;
        
    default:
        echo json_encode(['error' => 'Unknown action']);
}
