#!/usr/bin/php
<?php
/**
 * D-PlaneOS Automated Config Backup
 * Version: 1.13.0
 * 
 * Runs via cron to create scheduled backups
 */

$logFile = '/var/log/dplaneos-backup.log';

function logMessage($message) {
    global $logFile;
    $timestamp = date('Y-m-d H:i:s');
    file_put_contents($logFile, "[$timestamp] $message\n", FILE_APPEND);
}

try {
    logMessage("Starting automated backup...");
    
    // Call the backup API
    $ch = curl_init('http://localhost/api/backup.php?action=create');
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_POST, true);
    curl_setopt($ch, CURLOPT_TIMEOUT, 300); // 5 minute timeout
    
    $result = curl_exec($ch);
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    
    if ($httpCode !== 200) {
        throw new Exception("HTTP $httpCode - Backup failed");
    }
    
    $data = json_decode($result, true);
    
    if (!isset($data['success']) || !$data['success']) {
        throw new Exception($data['error'] ?? 'Unknown error');
    }
    
    logMessage("Backup created successfully: {$data['file']} ({$data['size_mb']} MB)");
    logMessage("Password hash stored in database");
    logMessage("Checksum: {$data['checksum']}");
    
    // Optional: Send notification email
    if (file_exists('/usr/bin/mail') && isset($data['file'])) {
        $hostname = gethostname();
        $subject = "D-PlaneOS Backup Successful - $hostname";
        $message = "Automated backup completed successfully.\n\n";
        $message .= "File: {$data['file']}\n";
        $message .= "Size: {$data['size_mb']} MB\n";
        $message .= "Time: " . date('Y-m-d H:i:s') . "\n";
        
        // Get admin email from settings
        $db = new SQLite3('/var/www/dplaneos/database.sqlite');
        $result = $db->querySingle("SELECT value FROM system_settings WHERE key = 'admin_email'");
        
        if ($result) {
            mail($result, $subject, $message);
            logMessage("Notification email sent to $result");
        }
    }
    
    logMessage("Automated backup completed successfully");
    exit(0);
    
} catch (Exception $e) {
    logMessage("ERROR: " . $e->getMessage());
    
    // Send error notification
    if (file_exists('/usr/bin/mail')) {
        $hostname = gethostname();
        $subject = "D-PlaneOS Backup FAILED - $hostname";
        $message = "Automated backup failed.\n\n";
        $message .= "Error: " . $e->getMessage() . "\n";
        $message .= "Time: " . date('Y-m-d H:i:s') . "\n";
        $message .= "\nPlease check /var/log/dplaneos-backup.log for details.";
        
        $db = new SQLite3('/var/www/dplaneos/database.sqlite');
        $result = $db->querySingle("SELECT value FROM system_settings WHERE key = 'admin_email'");
        
        if ($result) {
            mail($result, $subject, $message);
        }
    }
    
    exit(1);
}
?>
