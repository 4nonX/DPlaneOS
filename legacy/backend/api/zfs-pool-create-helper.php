<?php
/**
 * D-PlaneOS ZFS Pool Creation Enhancement
 * Version: 1.13.0-FINAL
 * 
 * Sets autoexpand=on by default when creating pools
 * Add this to your existing ZFS pool creation code
 */

function createZFSPool($poolName, $vdevType, $devices) {
    // Build zpool create command
    $cmd = "zpool create";
    
    // Add options
    $cmd .= " -o ashift=12"; // 4K sectors
    $cmd .= " -o autoexpand=on"; // AUTO-EXPAND BY DEFAULT!
    $cmd .= " -O compression=lz4"; // Enable compression
    $cmd .= " -O atime=off"; // Disable access time updates
    $cmd .= " -O relatime=on"; // But keep relative atime
    
    // Add pool name
    $cmd .= " " . escapeshellarg($poolName);
    
    // Add vdev type (mirror, raidz1, raidz2, raidz3)
    if ($vdevType !== 'stripe') {
        $cmd .= " $vdevType";
    }
    
    // Add devices
    foreach ($devices as $device) {
        $cmd .= " " . escapeshellarg($device);
    }
    
    // Execute command
    exec($cmd . " 2>&1", $output, $returnCode);
    
    if ($returnCode !== 0) {
        throw new Exception('Pool creation failed: ' . implode("\n", $output));
    }
    
    // Log the creation
    $db = new SQLite3('/var/www/dplaneos/database.sqlite');
    $stmt = $db->prepare('INSERT INTO disk_actions (pool, device, action, details, created_at) VALUES (?, ?, ?, ?, datetime("now"))');
    $stmt->bindValue(1, $poolName);
    $stmt->bindValue(2, implode(',', $devices));
    $stmt->bindValue(3, 'create_pool');
    $stmt->bindValue(4, "Pool created with $vdevType vdev, autoexpand enabled");
    $stmt->execute();
    
    return [
        'success' => true,
        'pool' => $poolName,
        'message' => "Pool $poolName created successfully with auto-expand enabled"
    ];
}

// Example usage in your ZFS API:
/*
if ($action === 'create_pool') {
    $poolName = $_POST['pool_name'] ?? '';
    $vdevType = $_POST['vdev_type'] ?? 'mirror';
    $devices = json_decode($_POST['devices'] ?? '[]', true);
    
    $result = createZFSPool($poolName, $vdevType, $devices);
    echo json_encode($result);
}
*/
?>
