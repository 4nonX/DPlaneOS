<?php
/**
 * D-PlaneOS v1.14.0 - System Notifications
 * Persistent notification system with remote monitoring hooks (Telegram, etc.)
 */

class NotificationSystem {
    private $dbFile = '/var/lib/dplaneos/notifications.db';
    private $db;
    
    public function __construct() {
        // Ensure directory exists
        $dir = dirname($this->dbFile);
        if (!is_dir($dir)) {
            mkdir($dir, 0755, true);
        }
        
        // Initialize SQLite database
        $this->db = new SQLite3($this->dbFile);
        
        // Create notifications table
        $this->db->exec('CREATE TABLE IF NOT EXISTS notifications (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp INTEGER NOT NULL,
            level TEXT NOT NULL,
            category TEXT NOT NULL,
            title TEXT NOT NULL,
            message TEXT NOT NULL,
            data TEXT,
            read INTEGER DEFAULT 0,
            sent_remote INTEGER DEFAULT 0,
            created_at INTEGER DEFAULT (strftime("%s", "now"))
        )');
        
        // Create index for faster queries
        $this->db->exec('CREATE INDEX IF NOT EXISTS idx_timestamp ON notifications(timestamp DESC)');
        $this->db->exec('CREATE INDEX IF NOT EXISTS idx_read ON notifications(read)');
    }
    
    /**
     * Create new notification
     * 
     * @param array $data [
     *   'type' => 'success|info|warning|error|critical',
     *   'title' => 'Notification title',
     *   'message' => 'Notification message',
     *   'category' => 'storage|docker|network|system|backup',
     *   'data' => [additional data]
     * ]
     */
    public function create($data) {
        $type = $data['type'] ?? 'info';
        $title = $data['title'] ?? 'Notification';
        $message = $data['message'] ?? '';
        $category = $data['category'] ?? 'system';
        $additionalData = isset($data['data']) ? json_encode($data['data']) : null;
        
        $stmt = $this->db->prepare('
            INSERT INTO notifications (timestamp, level, category, title, message, data)
            VALUES (:timestamp, :level, :category, :title, :message, :data)
        ');
        
        $stmt->bindValue(':timestamp', time(), SQLITE3_INTEGER);
        $stmt->bindValue(':level', $type, SQLITE3_TEXT);
        $stmt->bindValue(':category', $category, SQLITE3_TEXT);
        $stmt->bindValue(':title', $title, SQLITE3_TEXT);
        $stmt->bindValue(':message', $message, SQLITE3_TEXT);
        $stmt->bindValue(':data', $additionalData, SQLITE3_TEXT);
        
        $result = $stmt->execute();
        $id = $this->db->lastInsertRowID();
        
        // Send to remote monitoring if critical/error
        if (in_array($type, ['critical', 'error'])) {
            $this->sendToRemote([
                'id' => $id,
                'type' => $type,
                'title' => $title,
                'message' => $message,
                'category' => $category,
                'timestamp' => time()
            ]);
        }
        
        return $id;
    }
    
    /**
     * Get recent notifications
     */
    public function getRecent($limit = 50, $unreadOnly = false) {
        $query = 'SELECT * FROM notifications';
        
        if ($unreadOnly) {
            $query .= ' WHERE read = 0';
        }
        
        $query .= ' ORDER BY timestamp DESC LIMIT ' . (int)$limit;
        
        $result = $this->db->query($query);
        $notifications = [];
        
        while ($row = $result->fetchArray(SQLITE3_ASSOC)) {
            $row['data'] = json_decode($row['data'], true);
            $notifications[] = $row;
        }
        
        return $notifications;
    }
    
    /**
     * Mark notification as read
     */
    public function markAsRead($id) {
        $stmt = $this->db->prepare('UPDATE notifications SET read = 1 WHERE id = :id');
        $stmt->bindValue(':id', $id, SQLITE3_INTEGER);
        return $stmt->execute();
    }
    
    /**
     * Mark all as read
     */
    public function markAllAsRead() {
        return $this->db->exec('UPDATE notifications SET read = 1 WHERE read = 0');
    }
    
    /**
     * Get unread count
     */
    public function getUnreadCount() {
        $result = $this->db->query('SELECT COUNT(*) as count FROM notifications WHERE read = 0');
        $row = $result->fetchArray(SQLITE3_ASSOC);
        return (int)$row['count'];
    }
    
    /**
     * Delete old notifications
     */
    public function cleanup($daysOld = 30) {
        $timestamp = time() - ($daysOld * 86400);
        $stmt = $this->db->prepare('DELETE FROM notifications WHERE timestamp < :timestamp');
        $stmt->bindValue(':timestamp', $timestamp, SQLITE3_INTEGER);
        return $stmt->execute();
    }
    
    /**
     * Send to remote monitoring systems (Telegram, etc.)
     * Stores in queue for external API callers
     */
    private function sendToRemote($notification) {
        // Write to remote monitoring queue
        $queueFile = '/var/lib/dplaneos/remote-queue.log';
        
        file_put_contents(
            $queueFile,
            json_encode($notification) . "\n",
            FILE_APPEND
        );
        
        // Mark as sent
        $stmt = $this->db->prepare('UPDATE notifications SET sent_remote = 1 WHERE id = :id');
        $stmt->bindValue(':id', $notification['id'], SQLITE3_INTEGER);
        $stmt->execute();
        
        // Hook for future Telegram/API integration
        // This file will be read by external monitoring service
    }
    
    /**
     * Get remote monitoring queue for external API callers
     */
    public function getRemoteQueue() {
        $queueFile = '/var/lib/dplaneos/remote-queue.log';
        
        if (!file_exists($queueFile)) {
            return [];
        }
        
        $lines = file($queueFile, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
        $queue = [];
        
        foreach ($lines as $line) {
            $queue[] = json_decode($line, true);
        }
        
        return $queue;
    }
    
    /**
     * Clear remote monitoring queue (after sent)
     */
    public function clearRemoteQueue() {
        $queueFile = '/var/lib/dplaneos/remote-queue.log';
        if (file_exists($queueFile)) {
            unlink($queueFile);
        }
    }
}
