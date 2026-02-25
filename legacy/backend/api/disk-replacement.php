<?php
/**
 * D-PlaneOS Disk Replacement Wizard API
 * Version: 1.13.0
 * 
 * UI-guided disk replacement with resilver tracking
 */

header('Content-Type: application/json');
require_once __DIR__ . '/auth.php';
require_once __DIR__ . '/database.php';

if (!isAdmin()) {
    http_response_code(403);
    echo json_encode(['error' => 'Admin access required']);
    exit;
}

$action = $_GET['action'] ?? $_POST['action'] ?? 'status';

try {
    switch($action) {
        case 'status':
            getPoolStatus();
            break;
        case 'identify-failed':
            identifyFailedDisks();
            break;
        case 'offline':
            offlineDisk();
            break;
        case 'scan-new':
            scanNewDisks();
            break;
        case 'replace':
            replaceDisk();
            break;
        case 'resilver-progress':
            resilverProgress();
            break;
        case 'complete':
            completeReplacement();
            break;
        case 'expand-pool':
            expandPool();
            break;
        default:
            throw new Exception('Invalid action');
    }
} catch (Exception $e) {
    http_response_code(500);
    echo json_encode(['error' => $e->getMessage()]);
}

function expandPool() {
    $pool = $_POST['pool'] ?? '';
    
    if (empty($pool)) {
        throw new Exception('Pool name required');
    }
    
    // Enable autoexpand
    exec("zpool set autoexpand=on $pool 2>&1", $output1, $returnCode1);
    if ($returnCode1 !== 0) {
        throw new Exception('Failed to enable autoexpand: ' . implode("\n", $output1));
    }
    
    // Get all devices in pool
    exec("zpool status -L $pool | grep '/dev/' | awk '{print \$1}' 2>&1", $devices);
    
    // Online each device with -e flag to expand
    foreach ($devices as $device) {
        if (!empty($device) && strpos($device, '/dev/') === 0) {
            exec("zpool online -e $pool $device 2>&1", $onlineOutput);
        }
    }
    
    sleep(2); // Wait for expansion
    
    // Get new pool size
    exec("zpool list -H -o size $pool 2>&1", $newSize);
    
    // Log the expansion
    logDiskAction($pool, 'all', 'expand', "Pool expanded to " . ($newSize[0] ?? 'unknown'));
    
    echo json_encode([
        'success' => true,
        'pool' => $pool,
        'new_size' => $newSize[0] ?? 'unknown'
    ]);
}

function getPoolStatus() {
    exec('zpool status -v 2>&1', $output, $returnCode);
    
    if ($returnCode !== 0) {
        throw new Exception('Failed to get pool status');
    }
    
    $pools = parseZpoolStatus(implode("\n", $output));
    
    echo json_encode([
        'pools' => $pools,
        'raw_output' => implode("\n", $output)
    ]);
}

function parseZpoolStatus($output) {
    $pools = [];
    $currentPool = null;
    $lines = explode("\n", $output);
    
    foreach ($lines as $line) {
        // Pool name line
        if (preg_match('/^\s*pool:\s+(.+)$/', $line, $matches)) {
            $currentPool = ['name' => trim($matches[1]), 'devices' => [], 'status' => 'UNKNOWN'];
            $pools[] = &$currentPool;
        }
        
        // State line
        if ($currentPool && preg_match('/^\s*state:\s+(.+)$/', $line, $matches)) {
            $currentPool['status'] = trim($matches[1]);
        }
        
        // Device lines (look for FAULTED, DEGRADED, UNAVAIL)
        if ($currentPool && preg_match('/^\s+(\S+)\s+(ONLINE|DEGRADED|FAULTED|UNAVAIL|OFFLINE)/', $line, $matches)) {
            $device = trim($matches[1]);
            $status = trim($matches[2]);
            
            // Skip non-disk entries
            if (!in_array($device, ['mirror-0', 'raidz1-0', 'raidz2-0', 'raidz3-0'])) {
                $currentPool['devices'][] = [
                    'name' => $device,
                    'status' => $status,
                    'is_failed' => in_array($status, ['FAULTED', 'UNAVAIL'])
                ];
            }
        }
    }
    
    return $pools;
}

function identifyFailedDisks() {
    $pool = $_GET['pool'] ?? '';
    if (empty($pool)) {
        throw new Exception('Pool name required');
    }
    
    exec("zpool status $pool 2>&1", $output);
    $statusText = implode("\n", $output);
    
    // Parse for failed disks with error counts
    $failedDisks = [];
    $lines = explode("\n", $statusText);
    
    foreach ($lines as $line) {
        if (preg_match('/^\s+(\S+)\s+(FAULTED|UNAVAIL|DEGRADED)/', $line, $matches)) {
            $device = $matches[1];
            
            // Get smart info if available
            $smartInfo = getSmartInfo($device);
            
            // Get error counts
            preg_match('/(\d+)\s+(\d+)\s+(\d+)$/', $line, $errorMatches);
            
            $failedDisks[] = [
                'device' => $device,
                'status' => $matches[2],
                'errors' => [
                    'read' => isset($errorMatches[1]) ? intval($errorMatches[1]) : 0,
                    'write' => isset($errorMatches[2]) ? intval($errorMatches[2]) : 0,
                    'checksum' => isset($errorMatches[3]) ? intval($errorMatches[3]) : 0
                ],
                'smart' => $smartInfo
            ];
        }
    }
    
    echo json_encode([
        'pool' => $pool,
        'failed_disks' => $failedDisks
    ]);
}

function getSmartInfo($device) {
    // Extract actual device path (remove partition numbers)
    $devicePath = preg_replace('/-part\d+$/', '', $device);
    
    exec("smartctl -i $devicePath 2>/dev/null", $output, $returnCode);
    
    if ($returnCode !== 0) {
        return null;
    }
    
    $info = [];
    foreach ($output as $line) {
        if (preg_match('/Serial Number:\s+(.+)/', $line, $matches)) {
            $info['serial'] = trim($matches[1]);
        }
        if (preg_match('/Model Family:\s+(.+)/', $line, $matches)) {
            $info['model'] = trim($matches[1]);
        }
        if (preg_match('/User Capacity:\s+(.+)/', $line, $matches)) {
            $info['capacity'] = trim($matches[1]);
        }
    }
    
    return $info;
}

function offlineDisk() {
    $pool = $_POST['pool'] ?? '';
    $device = $_POST['device'] ?? '';
    
    if (empty($pool) || empty($device)) {
        throw new Exception('Pool and device required');
    }
    
    // Safety check: don't offline if pool is already degraded
    exec("zpool status $pool | grep state:", $output);
    if (strpos(implode('', $output), 'DEGRADED') !== false) {
        // Already degraded, allow offline
    }
    
    exec("zpool offline $pool $device 2>&1", $output, $returnCode);
    
    if ($returnCode !== 0) {
        throw new Exception('Failed to offline disk: ' . implode("\n", $output));
    }
    
    // Log the action
    logDiskAction($pool, $device, 'offline', implode("\n", $output));
    
    echo json_encode([
        'success' => true,
        'message' => "Disk $device taken offline from pool $pool",
        'output' => implode("\n", $output)
    ]);
}

function scanNewDisks() {
    // Rescan SCSI bus for new disks
    exec('echo "- - -" > /sys/class/scsi_host/host0/scan 2>/dev/null');
    exec('echo "- - -" > /sys/class/scsi_host/host1/scan 2>/dev/null');
    exec('echo "- - -" > /sys/class/scsi_host/host2/scan 2>/dev/null');
    
    sleep(2); // Wait for kernel to detect new devices
    
    // Get list of all block devices
    exec('lsblk -J -o NAME,SIZE,TYPE,MOUNTPOINT,SERIAL 2>&1', $output);
    $lsblk = json_decode(implode('', $output), true);
    
    // Get ZFS devices
    exec('zpool status -L | grep -E "^\s+/dev/" | awk \'{print $1}\'', $zfsDevices);
    $usedDevices = array_map('trim', $zfsDevices);
    
    // Find available disks
    $availableDisks = [];
    
    if (isset($lsblk['blockdevices'])) {
        foreach ($lsblk['blockdevices'] as $device) {
            $devicePath = '/dev/' . $device['name'];
            
            // Check if disk (not partition), not mounted, not in ZFS
            if ($device['type'] === 'disk' && 
                empty($device['mountpoint']) && 
                !in_array($devicePath, $usedDevices)) {
                
                $availableDisks[] = [
                    'device' => $devicePath,
                    'size' => $device['size'],
                    'serial' => $device['serial'] ?? 'Unknown'
                ];
            }
        }
    }
    
    echo json_encode([
        'available_disks' => $availableDisks,
        'used_devices' => $usedDevices
    ]);
}

function replaceDisk() {
    $pool = $_POST['pool'] ?? '';
    $oldDevice = $_POST['old_device'] ?? '';
    $newDevice = $_POST['new_device'] ?? '';
    
    if (empty($pool) || empty($oldDevice) || empty($newDevice)) {
        throw new Exception('Pool, old device, and new device required');
    }
    
    // Verify new disk is not in use
    exec("zpool status -L | grep $newDevice", $checkOutput);
    if (!empty($checkOutput)) {
        throw new Exception('New disk is already in use by ZFS');
    }
    
    // Start replacement
    exec("zpool replace $pool $oldDevice $newDevice 2>&1", $output, $returnCode);
    
    if ($returnCode !== 0) {
        throw new Exception('Disk replacement failed: ' . implode("\n", $output));
    }
    
    // Log the action
    logDiskAction($pool, $oldDevice, 'replace', "Replaced with $newDevice");
    
    // Create replacement tracking entry
    $db = getDatabase();
    $stmt = $db->prepare('INSERT INTO disk_replacements (pool, old_device, new_device, started_at, status) VALUES (?, ?, ?, datetime("now"), "resilvering")');
    $stmt->bindValue(1, $pool);
    $stmt->bindValue(2, $oldDevice);
    $stmt->bindValue(3, $newDevice);
    $stmt->execute();
    
    $replacementId = $db->lastInsertRowID();
    
    echo json_encode([
        'success' => true,
        'message' => 'Disk replacement started. Resilvering in progress...',
        'replacement_id' => $replacementId,
        'output' => implode("\n", $output)
    ]);
}

function resilverProgress() {
    $pool = $_GET['pool'] ?? '';
    
    if (empty($pool)) {
        throw new Exception('Pool name required');
    }
    
    exec("zpool status $pool 2>&1", $output);
    $statusText = implode("\n", $output);
    
    // Parse resilver progress
    $progress = [
        'is_resilvering' => false,
        'percent' => 0,
        'scanned' => '0',
        'to_scan' => '0',
        'speed' => '0',
        'time_remaining' => 'unknown'
    ];
    
    if (preg_match('/resilver in progress/', $statusText)) {
        $progress['is_resilvering'] = true;
        
        // Extract progress percentage
        if (preg_match('/(\d+\.\d+)% done/', $statusText, $matches)) {
            $progress['percent'] = floatval($matches[1]);
        }
        
        // Extract scanned info
        if (preg_match('/(\S+)\s+scanned.*?of\s+(\S+)/', $statusText, $matches)) {
            $progress['scanned'] = $matches[1];
            $progress['to_scan'] = $matches[2];
        }
        
        // Extract speed
        if (preg_match('/(\S+)\/s/', $statusText, $matches)) {
            $progress['speed'] = $matches[1];
        }
        
        // Extract time remaining
        if (preg_match('/(\d+h\d+m|\d+m\d+s|\d+ days)/', $statusText, $matches)) {
            $progress['time_remaining'] = $matches[1];
        }
    }
    
    // Check if resilver completed
    if (preg_match('/resilver.*?completed/', $statusText)) {
        $progress['is_resilvering'] = false;
        $progress['percent'] = 100;
        
        // Update database
        $db = getDatabase();
        $db->exec("UPDATE disk_replacements SET status = 'completed', completed_at = datetime('now') WHERE pool = '$pool' AND status = 'resilvering'");
    }
    
    echo json_encode($progress);
}

function completeReplacement() {
    $replacementId = $_POST['replacement_id'] ?? 0;
    
    if (!$replacementId) {
        throw new Exception('Replacement ID required');
    }
    
    $db = getDatabase();
    
    // Get replacement details
    $stmt = $db->prepare('SELECT pool, old_device, new_device FROM disk_replacements WHERE id = ?');
    $stmt->bindValue(1, $replacementId, SQLITE3_INTEGER);
    $result = $stmt->execute();
    $replacement = $result->fetchArray(SQLITE3_ASSOC);
    
    if (!$replacement) {
        throw new Exception('Replacement not found');
    }
    
    // Check if pool can be expanded
    $expandInfo = checkAutoExpand($replacement['pool']);
    
    // === AUTO-EXPAND TRIGGER (AUTOMATIC) ===
    // Always enable autoexpand for future expansions
    exec("zpool set autoexpand=on " . escapeshellarg($replacement['pool']) . " 2>&1");
    
    // Trigger immediate expansion with new device
    exec("zpool online -e " . escapeshellarg($replacement['pool']) . " " . escapeshellarg($replacement['new_device']) . " 2>&1");
    
    // Log the auto-expand attempt
    logDiskAction($replacement['pool'], $replacement['new_device'], 'auto-expand', 'Triggered auto-expand after replacement');
    // === END AUTO-EXPAND ===
    
    // Mark replacement complete
    $stmt = $db->prepare('UPDATE disk_replacements SET status = "completed", completed_at = datetime("now") WHERE id = ?');
    $stmt->bindValue(1, $replacementId, SQLITE3_INTEGER);
    $stmt->execute();
    
    echo json_encode([
        'success' => true,
        'expand_available' => $expandInfo['can_expand'],
        'expand_info' => $expandInfo
    ]);
}

function checkAutoExpand($pool) {
    // Get pool size info
    exec("zpool list -H -o size,allocated,free $pool 2>&1", $output, $returnCode);
    
    if ($returnCode !== 0) {
        return ['can_expand' => false, 'reason' => 'Failed to get pool info'];
    }
    
    // Check if autoexpand is enabled
    exec("zpool get -H autoexpand $pool 2>&1", $autoexpandOutput);
    $autoexpandEnabled = false;
    foreach ($autoexpandOutput as $line) {
        if (preg_match('/autoexpand\s+(\w+)/', $line, $matches)) {
            $autoexpandEnabled = ($matches[1] === 'on');
        }
    }
    
    // Get all vdev sizes
    exec("zpool list -v $pool 2>&1", $vdevOutput);
    $diskSizes = [];
    $inVdev = false;
    
    foreach ($vdevOutput as $line) {
        // Skip pool summary line
        if (preg_match('/^\s*'.$pool.'\s+/', $line)) {
            $inVdev = true;
            continue;
        }
        
        // Parse disk lines
        if ($inVdev && preg_match('/^\s+(\/dev\/\S+)\s+\S+\s+\S+\s+\S+\s+(\S+)/', $line, $matches)) {
            $device = $matches[1];
            $size = $matches[2];
            $diskSizes[$device] = $size;
        }
    }
    
    // Check if all disks are same size (potential for expansion)
    if (empty($diskSizes)) {
        return ['can_expand' => false, 'reason' => 'Could not determine disk sizes'];
    }
    
    $sizes = array_values($diskSizes);
    $allSameSize = (count(array_unique($sizes)) === 1);
    
    // Check actual device capacities vs ZFS usage
    $hasUnusedSpace = false;
    foreach (array_keys($diskSizes) as $device) {
        // Get actual device size
        exec("blockdev --getsize64 $device 2>/dev/null", $blockdevOutput);
        if (!empty($blockdevOutput[0])) {
            $actualBytes = intval($blockdevOutput[0]);
            $actualGB = round($actualBytes / 1024 / 1024 / 1024, 2);
            
            // Get ZFS reported size
            $zfsSize = $diskSizes[$device];
            $zfsGB = parseSize($zfsSize);
            
            // If actual size is significantly larger, we have unused space
            if ($actualGB > ($zfsGB * 1.1)) { // 10% margin
                $hasUnusedSpace = true;
                break;
            }
        }
    }
    
    if ($hasUnusedSpace && $allSameSize) {
        return [
            'can_expand' => true,
            'autoexpand_enabled' => $autoexpandEnabled,
            'current_size' => $output[0] ?? 'Unknown',
            'recommendation' => $autoexpandEnabled ? 
                'Pool will auto-expand on next device addition' : 
                'Enable autoexpand and run: zpool online -e ' . $pool . ' <device>',
            'command' => "zpool set autoexpand=on $pool && zpool online -e $pool /dev/sdX"
        ];
    }
    
    return [
        'can_expand' => false,
        'reason' => $allSameSize ? 
            'All disks same size - no expansion available' : 
            'Mixed disk sizes - replace remaining smaller disks first'
    ];
}

function parseSize($sizeStr) {
    // Convert ZFS size string (e.g., "1.82T", "500G") to GB
    $units = ['K' => 0.001, 'M' => 1, 'G' => 1, 'T' => 1024, 'P' => 1024*1024];
    
    if (preg_match('/^(\d+\.?\d*)([KMGTP])/', $sizeStr, $matches)) {
        $value = floatval($matches[1]);
        $unit = $matches[2];
        return $value * ($units[$unit] ?? 1);
    }
    
    return 0;
}

function logDiskAction($pool, $device, $action, $details) {
    $db = getDatabase();
    $stmt = $db->prepare('INSERT INTO disk_actions (pool, device, action, details, created_at) VALUES (?, ?, ?, ?, datetime("now"))');
    $stmt->bindValue(1, $pool);
    $stmt->bindValue(2, $device);
    $stmt->bindValue(3, $action);
    $stmt->bindValue(4, $details);
    $stmt->execute();
}

function getDatabase() {
    static $db = null;
    if ($db === null) {
        $db = new SQLite3('/var/www/dplaneos/database.sqlite');
    }
    return $db;
}
?>
