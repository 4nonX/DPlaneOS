<?php
/**
 * D-PlaneOS Config Backup & Restore API
 * Version: 1.13.0
 * 
 * Provides encrypted backup/restore of entire system configuration
 */

header('Content-Type: application/json');
require_once __DIR__ . '/auth.php';
require_once __DIR__ . '/database.php';

// Admin-only access
if (!isAdmin()) {
    http_response_code(403);
    echo json_encode(['error' => 'Admin access required']);
    exit;
}

$action = $_GET['action'] ?? $_POST['action'] ?? 'list';

try {
    switch($action) {
        case 'create':
            createBackup();
            break;
        case 'download':
            downloadBackup($_GET['file'] ?? '');
            break;
        case 'list':
            listBackups();
            break;
        case 'restore':
            restoreBackup();
            break;
        case 'delete':
            deleteBackup($_GET['file'] ?? '');
            break;
        case 'schedule':
            scheduleBackup();
            break;
        case 'get_schedule':
            getSchedule();
            break;
        default:
            throw new Exception('Invalid action');
    }
} catch (Exception $e) {
    http_response_code(500);
    echo json_encode(['error' => $e->getMessage()]);
}

function createBackup() {
    $timestamp = date('Y-m-d_H-i-s');
    $backupDir = '/var/backups/dplaneos';
    $tempDir = "$backupDir/temp_$timestamp";
    $archiveName = "dplaneos-config-$timestamp.tar.gz.enc";
    
    // Ensure backup directory exists
    if (!is_dir($backupDir)) {
        mkdir($backupDir, 0700, true);
    }
    
    // Create temp directory
    mkdir($tempDir, 0700);
    
    try {
        // 1. SQLite Database (with proper locking)
        $db = getDatabase();
        $db->exec('PRAGMA wal_checkpoint(TRUNCATE)');
        copy('/var/www/dplaneos/database.sqlite', "$tempDir/database.sqlite");
        
        // 2. Docker Compose files
        if (is_dir('/var/www/dplaneos/docker')) {
            exec("cp -r /var/www/dplaneos/docker $tempDir/ 2>/dev/null");
        }
        
        // 3. Samba configuration
        if (file_exists('/etc/samba/smb.conf')) {
            copy('/etc/samba/smb.conf', "$tempDir/smb.conf");
        }
        if (is_dir('/var/lib/samba/usershares')) {
            exec("cp -r /var/lib/samba/usershares $tempDir/samba-shares 2>/dev/null");
        }
        
        // 4. NFS exports
        if (file_exists('/etc/exports')) {
            copy('/etc/exports', "$tempDir/nfs-exports");
        }
        
        // 5. Cron jobs
        exec("cp /etc/cron.d/dplaneos-* $tempDir/ 2>/dev/null");
        
        // 6. SSL certificates
        if (is_dir('/etc/ssl/dplaneos')) {
            exec("cp -r /etc/ssl/dplaneos $tempDir/ssl 2>/dev/null");
        }
        
        // 7. App Store repositories config
        if (file_exists('/var/www/dplaneos/app-store-repos.json')) {
            copy('/var/www/dplaneos/app-store-repos.json', "$tempDir/app-store-repos.json");
        }
        
        // 8. System metadata
        $metadata = [
            'hostname' => gethostname(),
            'version' => trim(file_get_contents('/var/www/dplaneos/VERSION')),
            'backup_date' => date('c'),
            'backup_version' => '1.13.0',
            'zfs_pools' => getZFSPools(),
            'installed_apps' => getInstalledApps(),
            'php_version' => PHP_VERSION,
            'docker_version' => trim(shell_exec('docker --version 2>/dev/null') ?: 'Not installed'),
            'system_info' => [
                'os' => php_uname('s'),
                'release' => php_uname('r'),
                'architecture' => php_uname('m')
            ]
        ];
        file_put_contents("$tempDir/metadata.json", json_encode($metadata, JSON_PRETTY_PRINT));
        
        // 9. Create checksums
        exec("cd $tempDir && find . -type f -exec sha256sum {} \\; > checksums.txt 2>/dev/null");
        
        // 10. Create tar archive
        exec("cd $backupDir && tar czf $archiveName.tmp -C temp_$timestamp . 2>&1", $output, $returnCode);
        if ($returnCode !== 0) {
            throw new Exception('Failed to create tar archive: ' . implode("\n", $output));
        }
        
        // 11. Encrypt with OpenSSL (AES-256-CBC)
        $password = bin2hex(random_bytes(16));
        $encryptCmd = "openssl enc -aes-256-cbc -salt -pbkdf2 -in $backupDir/$archiveName.tmp -out $backupDir/$archiveName -pass pass:" . escapeshellarg($password) . " 2>&1";
        exec($encryptCmd, $encOutput, $encReturn);
        
        if ($encReturn !== 0 || !file_exists("$backupDir/$archiveName")) {
            throw new Exception('Encryption failed: ' . implode("\n", $encOutput));
        }
        
        // 12. Cleanup temp files
        exec("rm -rf $tempDir");
        unlink("$backupDir/$archiveName.tmp");
        
        // 13. Save backup info to database
        $size = filesize("$backupDir/$archiveName");
        saveBackupInfo($archiveName, $password, $size, json_encode($metadata));
        
        // 14. Calculate checksum of encrypted file
        $checksum = hash_file('sha256', "$backupDir/$archiveName");
        
        echo json_encode([
            'success' => true,
            'file' => $archiveName,
            'size' => $size,
            'size_mb' => round($size / 1024 / 1024, 2),
            'password' => $password,
            'checksum' => $checksum,
            'metadata' => $metadata
        ]);
        
    } catch (Exception $e) {
        // Cleanup on error
        if (is_dir($tempDir)) {
            exec("rm -rf $tempDir");
        }
        if (file_exists("$backupDir/$archiveName.tmp")) {
            unlink("$backupDir/$archiveName.tmp");
        }
        throw $e;
    }
}

function saveBackupInfo($filename, $password, $size, $metadata) {
    $db = getDatabase();
    $stmt = $db->prepare('INSERT INTO config_backups (filename, password_hash, size, metadata, created_at) VALUES (?, ?, ?, ?, datetime("now"))');
    
    // Hash the password for storage (still need plaintext for user, but store hash)
    $passwordHash = password_hash($password, PASSWORD_BCRYPT);
    
    $stmt->bindValue(1, $filename, SQLITE3_TEXT);
    $stmt->bindValue(2, $passwordHash, SQLITE3_TEXT);
    $stmt->bindValue(3, $size, SQLITE3_INTEGER);
    $stmt->bindValue(4, $metadata, SQLITE3_TEXT);
    $stmt->execute();
}

function listBackups() {
    $db = getDatabase();
    $result = $db->query('SELECT id, filename, size, metadata, created_at FROM config_backups ORDER BY created_at DESC');
    
    $backups = [];
    while($row = $result->fetchArray(SQLITE3_ASSOC)) {
        $row['size_mb'] = round($row['size'] / 1024 / 1024, 2);
        $row['metadata'] = json_decode($row['metadata'], true);
        
        // Check if file actually exists
        $filepath = "/var/backups/dplaneos/{$row['filename']}";
        $row['file_exists'] = file_exists($filepath);
        
        if ($row['file_exists']) {
            $row['checksum'] = hash_file('sha256', $filepath);
        }
        
        $backups[] = $row;
    }
    
    echo json_encode(['backups' => $backups]);
}

function downloadBackup($filename) {
    // Security: validate filename format
    if (!preg_match('/^dplaneos-config-[\d-_]+\.tar\.gz\.enc$/', $filename)) {
        http_response_code(400);
        echo json_encode(['error' => 'Invalid filename format']);
        exit;
    }
    
    // Check if backup exists in database
    $db = getDatabase();
    $stmt = $db->prepare('SELECT * FROM config_backups WHERE filename = ?');
    $stmt->bindValue(1, $filename, SQLITE3_TEXT);
    $result = $stmt->execute();
    $backup = $result->fetchArray(SQLITE3_ASSOC);
    
    if (!$backup) {
        http_response_code(404);
        echo json_encode(['error' => 'Backup not found in database']);
        exit;
    }
    
    $filepath = "/var/backups/dplaneos/$filename";
    if (!file_exists($filepath)) {
        http_response_code(404);
        echo json_encode(['error' => 'Backup file not found on disk']);
        exit;
    }
    
    // Set headers for download
    header('Content-Type: application/octet-stream');
    header('Content-Disposition: attachment; filename="' . $filename . '"');
    header('Content-Length: ' . filesize($filepath));
    header('X-Backup-Checksum: ' . hash_file('sha256', $filepath));
    
    readfile($filepath);
    exit;
}

function restoreBackup() {
    // Check if file was uploaded
    if (!isset($_FILES['backup_file']) || $_FILES['backup_file']['error'] !== UPLOAD_ERR_OK) {
        throw new Exception('No backup file uploaded');
    }
    
    if (empty($_POST['password'])) {
        throw new Exception('Backup password required');
    }
    
    $uploadedFile = $_FILES['backup_file']['tmp_name'];
    $password = $_POST['password'];
    $timestamp = date('Y-m-d_H-i-s');
    $restoreDir = "/tmp/dplaneos-restore-$timestamp";
    
    try {
        // Create restore directory
        mkdir($restoreDir, 0700);
        
        // Decrypt the backup
        $decryptedFile = "$restoreDir/backup.tar.gz";
        $decryptCmd = "openssl enc -d -aes-256-cbc -pbkdf2 -in " . escapeshellarg($uploadedFile) . " -out $decryptedFile -pass pass:" . escapeshellarg($password) . " 2>&1";
        exec($decryptCmd, $decryptOutput, $decryptReturn);
        
        if ($decryptReturn !== 0 || !file_exists($decryptedFile)) {
            throw new Exception('Decryption failed. Invalid password?');
        }
        
        // Extract the archive
        exec("cd $restoreDir && tar xzf backup.tar.gz 2>&1", $extractOutput, $extractReturn);
        if ($extractReturn !== 0) {
            throw new Exception('Extraction failed: ' . implode("\n", $extractOutput));
        }
        
        // Verify checksums
        exec("cd $restoreDir && sha256sum -c checksums.txt 2>&1", $checksumOutput, $checksumReturn);
        if ($checksumReturn !== 0) {
            throw new Exception('Checksum verification failed! Backup may be corrupted.');
        }
        
        // Read metadata
        if (!file_exists("$restoreDir/metadata.json")) {
            throw new Exception('Backup metadata not found');
        }
        $metadata = json_decode(file_get_contents("$restoreDir/metadata.json"), true);
        
        // === DOCKER CLEANUP - BRUTAL BUT ROBUST ===
        $cleanupLog = [];
        $cleanupLog[] = "Cleaning Docker environment before restore...";
        
        // Get containers that will be restored
        $restoredApps = $metadata['installed_apps'] ?? [];
        $cleanupLog[] = "Backup contains " . count($restoredApps) . " apps to restore";
        
        // BRUTAL CLEANUP: Stop and remove ALL containers
        // This prevents any ghost-apps or zombies
        $cleanupLog[] = "Stopping all containers...";
        exec('docker stop $(docker ps -aq) 2>/dev/null', $stopOutput);
        
        $cleanupLog[] = "Removing all containers...";
        exec('docker rm $(docker ps -aq) 2>/dev/null', $rmOutput);
        
        // Prune unused networks to prevent IP conflicts
        $cleanupLog[] = "Pruning unused Docker networks...";
        exec('docker network prune -f 2>/dev/null', $netOutput);
        
        // Note: Images are kept for speed (no re-download needed)
        $cleanupLog[] = "Docker environment cleaned (images preserved for speed)";
        
        // === END DOCKER CLEANUP ===
        
        // Backup current configuration before restore
        $preRestoreBackup = "/var/backups/dplaneos/pre-restore-" . date('Y-m-d_H-i-s') . ".sqlite";
        copy('/var/www/dplaneos/database.sqlite', $preRestoreBackup);
        $cleanupLog[] = "Pre-restore backup saved: $preRestoreBackup";
        
        // Restore database
        if (file_exists("$restoreDir/database.sqlite")) {
            copy("$restoreDir/database.sqlite", '/var/www/dplaneos/database.sqlite');
            chown('/var/www/dplaneos/database.sqlite', 'www-data');
            chgrp('/var/www/dplaneos/database.sqlite', 'www-data');
            $cleanupLog[] = "Database restored";
        }
        
        // Restore Docker configs
        if (is_dir("$restoreDir/docker")) {
            exec("rm -rf /var/www/dplaneos/docker");
            exec("cp -r $restoreDir/docker /var/www/dplaneos/");
            exec("chown -R www-data:www-data /var/www/dplaneos/docker");
            $cleanupLog[] = "Docker configs restored";
        }
        
        // Restore Samba
        if (file_exists("$restoreDir/smb.conf")) {
            copy("$restoreDir/smb.conf", '/etc/samba/smb.conf');
            exec("systemctl restart smbd nmbd 2>&1");
            $cleanupLog[] = "Samba config restored";
        }
        
        // Restore NFS
        if (file_exists("$restoreDir/nfs-exports")) {
            copy("$restoreDir/nfs-exports", '/etc/exports');
            exec("exportfs -ra 2>&1");
            $cleanupLog[] = "NFS exports restored";
        }
        
        // Restore SSL certs
        if (is_dir("$restoreDir/ssl")) {
            exec("rm -rf /etc/ssl/dplaneos");
            exec("cp -r $restoreDir/ssl /etc/ssl/dplaneos");
            $cleanupLog[] = "SSL certificates restored";
        }
        
        // Restore cron jobs
        exec("rm -f /etc/cron.d/dplaneos-*");
        exec("cp $restoreDir/dplaneos-* /etc/cron.d/ 2>/dev/null");
        $cleanupLog[] = "Cron jobs restored";
        
        // === RESTART CONTAINERS FROM BACKUP ===
        $startLog = [];
        
        if (!empty($restoredApps)) {
            $startLog[] = "Restarting " . count($restoredApps) . " containers from backup:";
            
            foreach ($restoredApps as $app) {
                $composeFile = "/var/www/dplaneos/docker/$app/docker-compose.yml";
                if (file_exists($composeFile)) {
                    $startLog[] = "  Starting: $app";
                    exec("cd /var/www/dplaneos/docker/$app && docker-compose up -d 2>&1");
                }
            }
        }
        
        // Cleanup
        exec("rm -rf $restoreDir");
        
        echo json_encode([
            'success' => true,
            'message' => 'Configuration restored successfully',
            'metadata' => $metadata,
            'pre_restore_backup' => $preRestoreBackup,
            'cleanup_log' => $cleanupLog,
            'start_log' => $startLog,
            'zombie_containers_removed' => count($zombieContainers)
        ]);
        
    } catch (Exception $e) {
        // Cleanup on error
        if (is_dir($restoreDir)) {
            exec("rm -rf $restoreDir");
        }
        throw $e;
    }
}

function deleteBackup($filename) {
    // Security: validate filename
    if (!preg_match('/^dplaneos-config-[\d-_]+\.tar\.gz\.enc$/', $filename)) {
        http_response_code(400);
        echo json_encode(['error' => 'Invalid filename']);
        exit;
    }
    
    $db = getDatabase();
    $stmt = $db->prepare('DELETE FROM config_backups WHERE filename = ?');
    $stmt->bindValue(1, $filename, SQLITE3_TEXT);
    $stmt->execute();
    
    $filepath = "/var/backups/dplaneos/$filename";
    if (file_exists($filepath)) {
        unlink($filepath);
    }
    
    echo json_encode(['success' => true]);
}

function scheduleBackup() {
    $frequency = $_POST['frequency'] ?? 'daily';
    $time = $_POST['time'] ?? '02:00';
    $keepDays = intval($_POST['keep_days'] ?? 30);
    
    // Parse time
    list($hour, $minute) = explode(':', $time);
    
    // Create cron expression
    $cronExpr = match($frequency) {
        'daily' => "$minute $hour * * *",
        'weekly' => "$minute $hour * * 0",
        'monthly' => "$minute $hour 1 * *",
        default => "$minute $hour * * *"
    };
    
    // Create cron file
    $cronContent = "# D-PlaneOS Automated Config Backup\n";
    $cronContent .= "$cronExpr www-data /usr/bin/php /var/www/dplaneos/scripts/auto-backup.php\n";
    $cronContent .= "# Cleanup old backups\n";
    $cronContent .= "0 3 * * * root find /var/backups/dplaneos -name '*.tar.gz.enc' -mtime +$keepDays -delete\n";
    
    file_put_contents('/etc/cron.d/dplaneos-backup', $cronContent);
    
    // Save schedule to database
    $db = getDatabase();
    $db->exec("INSERT OR REPLACE INTO system_settings (key, value) VALUES ('backup_schedule', " . $db->escapeString(json_encode([
        'frequency' => $frequency,
        'time' => $time,
        'keep_days' => $keepDays,
        'enabled' => true
    ])) . ")");
    
    echo json_encode(['success' => true, 'schedule' => $frequency]);
}

function getSchedule() {
    $db = getDatabase();
    $stmt = $db->prepare("SELECT value FROM system_settings WHERE key = 'backup_schedule'");
    $result = $stmt->execute();
    $row = $result->fetchArray(SQLITE3_ASSOC);
    if ($row) {
        echo json_encode(json_decode($row['value'], true));
    } else {
        echo json_encode(['enabled' => false]);
    }
}

function getZFSPools() {
    exec('zpool list -H -o name 2>/dev/null', $output);
    return array_filter($output);
}

function getInstalledApps() {
    $db = getDatabase();
    $result = $db->query('SELECT name FROM docker_apps WHERE status = "running"');
    $apps = [];
    while($row = $result->fetchArray(SQLITE3_ASSOC)) {
        $apps[] = $row['name'];
    }
    return $apps;
}

function getDatabase() {
    static $db = null;
    if ($db === null) {
        $db = new SQLite3('/var/www/dplaneos/database.sqlite');
    }
    return $db;
}
?>
