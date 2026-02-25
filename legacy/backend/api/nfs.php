<?php
/**
 * D-PlaneOS v1.14.0 - NFS Share Management
 * Real exportfs commands for NFS share management
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

require_once __DIR__ . '/notifications.php';

class NFSManager {
    private $notifications;
    private $exportsFile = '/etc/exports';
    
    public function __construct() {
        $this->notifications = new NotificationSystem();
    }
    
    private function exec($command, &$output = null, &$returnCode = null) {
        exec($command . ' 2>&1', $output, $returnCode);
        error_log("[NFS] $command => $returnCode");
        return $returnCode === 0;
    }
    
    public function listShares() {
        if (!file_exists($this->exportsFile)) {
            return ['shares' => []];
        }
        
        $shares = [];
        $lines = file($this->exportsFile, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
        
        foreach ($lines as $line) {
            $line = trim($line);
            if (empty($line) || $line[0] === '#') continue;
            
            // Parse: /path client(options)
            if (preg_match('/^([^\s]+)\s+(.+)$/', $line, $matches)) {
                $path = $matches[1];
                $clients = $matches[2];
                
                $shares[] = [
                    'path' => $path,
                    'clients' => $clients,
                    'raw' => $line
                ];
            }
        }
        
        return ['shares' => $shares];
    }
    
    public function addShare($data) {
        $path = $data['path'];
        $client = $data['client'] ?? '*';
        $options = $data['options'] ?? 'rw,sync,no_subtree_check';
        
        // Validate path exists
        if (!is_dir($path)) {
            return ['success' => false, 'error' => 'Path does not exist'];
        }
        
        // Create export line
        $export = "$path $client($options)";
        
        // Add to exports file
        file_put_contents($this->exportsFile, "\n$export\n", FILE_APPEND);
        
        // Reload exports
        if (!$this->exec('exportfs -ra', $output, $code)) {
            return ['success' => false, 'error' => 'Failed to reload exports: ' . implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'NFS Share Created',
            'message' => "NFS share created: $path",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function removeShare($path) {
        if (!file_exists($this->exportsFile)) {
            return ['success' => false, 'error' => 'Exports file not found'];
        }
        
        $lines = file($this->exportsFile, FILE_IGNORE_NEW_LINES);
        $newLines = [];
        $found = false;
        
        foreach ($lines as $line) {
            $trimmed = trim($line);
            if (!empty($trimmed) && strpos($trimmed, $path) === 0 && $trimmed[0] !== '#') {
                $found = true;
                continue; // Skip this line
            }
            $newLines[] = $line;
        }
        
        if (!$found) {
            return ['success' => false, 'error' => 'Share not found'];
        }
        
        file_put_contents($this->exportsFile, implode("\n", $newLines) . "\n");
        
        // Reload exports
        if (!$this->exec('exportfs -ra', $output, $code)) {
            return ['success' => false, 'error' => 'Failed to reload exports'];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'NFS Share Removed',
            'message' => "NFS share removed: $path",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function getActiveExports() {
        if (!$this->exec('exportfs -v', $output, $code)) {
            return ['exports' => []];
        }
        
        return ['exports' => implode("\n", $output)];
    }
    
    public function reloadExports() {
        if ($this->exec('exportfs -ra', $output, $code)) {
            return ['success' => true, 'message' => 'Exports reloaded'];
        }
        return ['success' => false, 'error' => implode("\n", $output)];
    }
}

$nfs = new NFSManager();
$action = $_GET['action'] ?? $_POST['action'] ?? 'list';

switch ($action) {
    case 'list':
        echo json_encode($nfs->listShares());
        break;
    
    case 'add':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($nfs->addShare($data));
        break;
    
    case 'remove':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($nfs->removeShare($data['path']));
        break;
    
    case 'active':
        echo json_encode($nfs->getActiveExports());
        break;
    
    case 'reload':
        echo json_encode($nfs->reloadExports());
        break;
    
    default:
        echo json_encode(['error' => 'Unknown action']);
}
