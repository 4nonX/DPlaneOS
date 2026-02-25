<?php
/**
 * D-PlaneOS v1.14.0 - Complete ZFS Management API
 * REAL ZFS commands - No mockups, no theatre
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

require_once __DIR__ . '/notifications.php';

$action = $_GET['action'] ?? $_POST['action'] ?? 'list_pools';
$notifications = new NotificationSystem();

// ==========================================
// POOL OPERATIONS
// ==========================================

function listPools() {
    $output = shell_exec('zpool list -H -o name,size,alloc,free,cap,health,dedup,frag 2>&1');
    
    if (empty($output) || strpos($output, 'no pools available') !== false) {
        return ['pools' => []];
    }
    
    $pools = [];
    $lines = explode("\n", trim($output));
    
    foreach ($lines as $line) {
        if (empty($line)) continue;
        
        $parts = preg_split('/\s+/', $line);
        if (count($parts) < 6) continue;
        
        $poolName = $parts[0];
        
        // Get detailed status
        $status = shell_exec("zpool status $poolName 2>&1");
        $vdevCount = substr_count($status, 'mirror-') + substr_count($status, 'raidz');
        
        $pools[] = [
            'name' => $poolName,
            'size' => $parts[1],
            'alloc' => $parts[2],
            'free' => $parts[3],
            'cap' => rtrim($parts[4], '%'),
            'health' => $parts[5],
            'dedup' => $parts[6] ?? '1.00x',
            'frag' => $parts[7] ?? '0%',
            'vdevs' => $vdevCount,
            'status' => getPoolDetailedStatus($poolName)
        ];
    }
    
    return ['pools' => $pools, 'count' => count($pools)];
}

function getPoolDetailedStatus($poolName) {
    $output = shell_exec("zpool status $poolName 2>&1");
    return [
        'raw' => $output,
        'config' => parseZpoolStatus($output),
        'errors' => parseZpoolErrors($output)
    ];
}

function parseZpoolStatus($output) {
    $lines = explode("\n", $output);
    $config = [];
    $inConfig = false;
    
    foreach ($lines as $line) {
        if (strpos($line, 'config:') !== false) {
            $inConfig = true;
            continue;
        }
        
        if ($inConfig && strpos($line, 'errors:') !== false) {
            break;
        }
        
        if ($inConfig && !empty(trim($line))) {
            $config[] = trim($line);
        }
    }
    
    return $config;
}

function parseZpoolErrors($output) {
    if (preg_match('/errors: (.+)$/m', $output, $matches)) {
        return trim($matches[1]);
    }
    return 'No known data errors';
}

function createPool($data) {
    global $notifications;
    
    $name = escapeshellarg($data['name']);
    $type = $data['type']; // stripe, mirror, raidz1, raidz2, raidz3
    $disks = $data['disks'];
    $options = $data['options'] ?? [];
    
    // Validate
    if (empty($disks)) {
        return ['success' => false, 'error' => 'No disks selected'];
    }
    
    // Build command
    $cmd = "zpool create -f $name";
    
    // Add options
    if (isset($options['ashift'])) {
        $ashift = (int)$options['ashift'];
        $cmd .= " -o ashift=$ashift";
    }
    
    if (isset($options['compression']) && $options['compression'] !== 'off') {
        $compression = escapeshellarg($options['compression']);
        $cmd .= " -O compression=$compression";
    }
    
    if (isset($options['dedup']) && $options['dedup'] === true) {
        $cmd .= " -O dedup=on";
    }
    
    if (isset($options['encryption']) && $options['encryption'] === true) {
        $cmd .= " -O encryption=on -O keyformat=passphrase";
    }
    
    if (isset($options['mountpoint'])) {
        $mountpoint = escapeshellarg($options['mountpoint']);
        $cmd .= " -m $mountpoint";
    }
    
    // Add RAID type
    if ($type !== 'stripe') {
        $cmd .= " $type";
    }
    
    // Add disks
    foreach ($disks as $disk) {
        $cmd .= " " . escapeshellarg($disk);
    }
    
    // Execute
    exec($cmd . ' 2>&1', $output, $returnCode);
    
    if ($returnCode === 0) {
        // Send notification
        $notifications->create([
            'type' => 'success',
            'title' => 'Pool Created',
            'message' => "ZFS pool '$name' created successfully with " . count($disks) . " disks",
            'category' => 'storage',
            'data' => ['pool' => $name, 'disks' => count($disks)]
        ]);
        
        return [
            'success' => true,
            'pool' => $name,
            'message' => 'Pool created successfully',
            'output' => implode("\n", $output)
        ];
    } else {
        $error = implode("\n", $output);
        
        $notifications->create([
            'type' => 'error',
            'title' => 'Pool Creation Failed',
            'message' => "Failed to create pool '$name': $error",
            'category' => 'storage'
        ]);
        
        return [
            'success' => false,
            'error' => $error,
            'command' => $cmd
        ];
    }
}

function destroyPool($poolName) {
    global $notifications;
    
    $pool = escapeshellarg($poolName);
    exec("zpool destroy $pool 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'warning',
            'title' => 'Pool Destroyed',
            'message' => "ZFS pool '$poolName' has been destroyed",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function exportPool($poolName) {
    global $notifications;
    
    $pool = escapeshellarg($poolName);
    exec("zpool export $pool 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'info',
            'title' => 'Pool Exported',
            'message' => "Pool '$poolName' exported successfully",
            'category' => 'storage'
        ]);
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function importPool($poolName) {
    global $notifications;
    
    $pool = escapeshellarg($poolName);
    exec("zpool import $pool 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'success',
            'title' => 'Pool Imported',
            'message' => "Pool '$poolName' imported successfully",
            'category' => 'storage'
        ]);
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function scrubPool($poolName) {
    global $notifications;
    
    $pool = escapeshellarg($poolName);
    exec("zpool scrub $pool 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'info',
            'title' => 'Scrub Started',
            'message' => "Scrub started on pool '$poolName'",
            'category' => 'storage'
        ]);
        return ['success' => true, 'message' => 'Scrub started'];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function getScrubStatus($poolName) {
    $pool = escapeshellarg($poolName);
    $output = shell_exec("zpool status $pool 2>&1");
    
    // Parse scrub status
    if (preg_match('/scrub: (.+?)$/m', $output, $matches)) {
        return ['status' => trim($matches[1])];
    }
    
    return ['status' => 'none in progress'];
}

// ==========================================
// DATASET OPERATIONS
// ==========================================

function listDatasets($poolName = null) {
    $cmd = "zfs list -H -o name,used,avail,refer,mountpoint";
    if ($poolName) {
        $cmd .= " " . escapeshellarg($poolName);
    }
    
    $output = shell_exec($cmd . " 2>&1");
    
    if (empty($output)) {
        return ['datasets' => []];
    }
    
    $datasets = [];
    $lines = explode("\n", trim($output));
    
    foreach ($lines as $line) {
        if (empty($line)) continue;
        
        $parts = preg_split('/\s+/', $line);
        if (count($parts) < 5) continue;
        
        $datasets[] = [
            'name' => $parts[0],
            'used' => $parts[1],
            'avail' => $parts[2],
            'refer' => $parts[3],
            'mountpoint' => $parts[4]
        ];
    }
    
    return ['datasets' => $datasets];
}

function createDataset($data) {
    global $notifications;
    
    $name = escapeshellarg($data['name']);
    $cmd = "zfs create";
    
    // Options
    if (isset($data['quota'])) {
        $quota = escapeshellarg($data['quota']);
        $cmd .= " -o quota=$quota";
    }
    
    if (isset($data['reservation'])) {
        $reservation = escapeshellarg($data['reservation']);
        $cmd .= " -o reservation=$reservation";
    }
    
    if (isset($data['compression'])) {
        $compression = escapeshellarg($data['compression']);
        $cmd .= " -o compression=$compression";
    }
    
    $cmd .= " $name";
    
    exec($cmd . ' 2>&1', $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'success',
            'title' => 'Dataset Created',
            'message' => "Dataset '$name' created successfully",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function destroyDataset($datasetName) {
    global $notifications;
    
    $dataset = escapeshellarg($datasetName);
    exec("zfs destroy $dataset 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'warning',
            'title' => 'Dataset Destroyed',
            'message' => "Dataset '$datasetName' has been destroyed",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

// ==========================================
// SNAPSHOT OPERATIONS
// ==========================================

function listSnapshots($datasetName = null) {
    $cmd = "zfs list -t snapshot -H -o name,used,refer,creation";
    if ($datasetName) {
        $cmd .= " -r " . escapeshellarg($datasetName);
    }
    
    $output = shell_exec($cmd . " 2>&1");
    
    if (empty($output)) {
        return ['snapshots' => []];
    }
    
    $snapshots = [];
    $lines = explode("\n", trim($output));
    
    foreach ($lines as $line) {
        if (empty($line)) continue;
        
        $parts = preg_split('/\s+/', $line, 4);
        if (count($parts) < 4) continue;
        
        $snapshots[] = [
            'name' => $parts[0],
            'used' => $parts[1],
            'refer' => $parts[2],
            'creation' => $parts[3]
        ];
    }
    
    return ['snapshots' => $snapshots];
}

function createSnapshot($data) {
    global $notifications;
    
    $dataset = escapeshellarg($data['dataset']);
    $snapname = escapeshellarg($data['name']);
    
    exec("zfs snapshot $dataset@$snapname 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'success',
            'title' => 'Snapshot Created',
            'message' => "Snapshot '$snapname' of dataset '$dataset' created",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function rollbackSnapshot($snapshotName) {
    global $notifications;
    
    $snapshot = escapeshellarg($snapshotName);
    exec("zfs rollback $snapshot 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'warning',
            'title' => 'Snapshot Rollback',
            'message' => "Rolled back to snapshot '$snapshotName'",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function cloneSnapshot($data) {
    global $notifications;
    
    $snapshot = escapeshellarg($data['snapshot']);
    $clone = escapeshellarg($data['clone']);
    
    exec("zfs clone $snapshot $clone 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'success',
            'title' => 'Snapshot Cloned',
            'message' => "Clone '$clone' created from snapshot '$snapshot'",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

function destroySnapshot($snapshotName) {
    global $notifications;
    
    $snapshot = escapeshellarg($snapshotName);
    exec("zfs destroy $snapshot 2>&1", $output, $returnCode);
    
    if ($returnCode === 0) {
        $notifications->create([
            'type' => 'info',
            'title' => 'Snapshot Destroyed',
            'message' => "Snapshot '$snapshotName' destroyed",
            'category' => 'storage'
        ]);
        
        return ['success' => true];
    } else {
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

// ==========================================
// ROUTE HANDLER
// ==========================================

try {
    $result = null;
    
    switch ($action) {
        // Pools
        case 'list_pools':
            $result = listPools();
            break;
        case 'create_pool':
            $data = json_decode(file_get_contents('php://input'), true);
            $result = createPool($data);
            break;
        case 'destroy_pool':
            $result = destroyPool($_POST['pool'] ?? $_GET['pool']);
            break;
        case 'export_pool':
            $result = exportPool($_POST['pool'] ?? $_GET['pool']);
            break;
        case 'import_pool':
            $result = importPool($_POST['pool'] ?? $_GET['pool']);
            break;
        case 'scrub_pool':
            $result = scrubPool($_POST['pool'] ?? $_GET['pool']);
            break;
        case 'scrub_status':
            $result = getScrubStatus($_POST['pool'] ?? $_GET['pool']);
            break;
            
        // Datasets
        case 'list_datasets':
            $result = listDatasets($_GET['pool'] ?? null);
            break;
        case 'create_dataset':
            $data = json_decode(file_get_contents('php://input'), true);
            $result = createDataset($data);
            break;
        case 'destroy_dataset':
            $result = destroyDataset($_POST['dataset'] ?? $_GET['dataset']);
            break;
            
        // Snapshots
        case 'list_snapshots':
            $result = listSnapshots($_GET['dataset'] ?? null);
            break;
        case 'create_snapshot':
            $data = json_decode(file_get_contents('php://input'), true);
            $result = createSnapshot($data);
            break;
        case 'rollback_snapshot':
            $result = rollbackSnapshot($_POST['snapshot'] ?? $_GET['snapshot']);
            break;
        case 'clone_snapshot':
            $data = json_decode(file_get_contents('php://input'), true);
            $result = cloneSnapshot($data);
            break;
        case 'destroy_snapshot':
            $result = destroySnapshot($_POST['snapshot'] ?? $_GET['snapshot']);
            break;

        // ─── Aliases — storage.html and main.js use short action names ───
        case 'list':
            $result = listPools();
            break;
        case 'create':
            $data = json_decode(file_get_contents('php://input'), true);
            $result = createPool($data);
            break;
        case 'scrub':
            $result = scrubPool($_POST['pool'] ?? $_GET['pool']);
            break;

        // ─── pool_status — raw zpool status output ───
        case 'pool_status': {
            $pool = $_GET['pool'] ?? $_POST['pool'] ?? '';
            $output = shell_exec('zpool status ' . escapeshellarg($pool) . ' 2>&1');
            $result = ['status' => $output ?: 'Pool not found'];
            break;
        }

        // ─── disks — list block devices for pool wizard ───
        case 'disks': {
            $raw = shell_exec('lsblk -J -o NAME,SIZE,TYPE,MOUNTPOINT,LABEL 2>/dev/null');
            $lsblk = json_decode($raw, true);
            $disks = [];
            foreach ($lsblk['blockdevices'] ?? [] as $dev) {
                if ($dev['type'] !== 'disk') continue;
                $name  = escapeshellarg($dev['name']);
                $inUse = trim(shell_exec("zpool status 2>/dev/null | grep -q $name && echo yes || echo no"));
                $disks[] = [
                    'name'  => $dev['name'],
                    'size'  => $dev['size'],
                    'label' => $dev['label'] ?? '',
                    'inUse' => ($inUse === 'yes'),
                ];
            }
            $result = ['disks' => $disks];
            break;
        }

        default:
            $result = ['error' => 'Unknown action: ' . $action];
    }
    
    echo json_encode($result);
    
} catch (Exception $e) {
    echo json_encode([
        'success' => false,
        'error' => $e->getMessage()
    ]);
}
