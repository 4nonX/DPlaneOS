<?php
/**
 * D-PlaneOS v1.14.0 - SMB/Samba Share Management
 * Real Samba commands for Windows file sharing
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

require_once __DIR__ . '/notifications.php';

class SMBManager {
    private $notifications;
    private $smbConf = '/etc/samba/smb.conf';
    
    public function __construct() {
        $this->notifications = new NotificationSystem();
    }
    
    private function exec($command, &$output = null, &$returnCode = null) {
        exec($command . ' 2>&1', $output, $returnCode);
        error_log("[SMB] $command => $returnCode");
        return $returnCode === 0;
    }
    
    public function listShares() {
        if (!$this->exec('net usershare list', $output, $code)) {
            // Fallback to parsing smb.conf
            return $this->parseSmartConf();
        }
        
        $shares = [];
        foreach ($output as $line) {
            if (empty($line)) continue;
            $shares[] = ['name' => trim($line)];
        }
        
        return ['shares' => $shares];
    }
    
    private function parseSmartConf() {
        if (!file_exists($this->smbConf)) {
            return ['shares' => []];
        }
        
        $content = file_get_contents($this->smbConf);
        $shares = [];
        
        // Parse [share_name] sections
        if (preg_match_all('/\[([^\]]+)\]/', $content, $matches)) {
            foreach ($matches[1] as $share) {
                if ($share !== 'global' && $share !== 'homes' && $share !== 'printers') {
                    $shares[] = ['name' => $share];
                }
            }
        }
        
        return ['shares' => $shares];
    }
    
    public function getShareDetails($shareName) {
        if (!$this->exec("net usershare info $shareName", $output, $code)) {
            return ['error' => 'Share not found'];
        }
        
        $details = [];
        foreach ($output as $line) {
            if (strpos($line, ':') !== false) {
                list($key, $value) = explode(':', $line, 2);
                $details[trim(strtolower($key))] = trim($value);
            }
        }
        
        return ['details' => $details];
    }
    
    public function addShare($data) {
        $name = $data['name'];
        $path = $data['path'];
        $comment = $data['comment'] ?? '';
        $guest = $data['guest'] ?? false;
        
        // Validate path
        if (!is_dir($path)) {
            return ['success' => false, 'error' => 'Path does not exist'];
        }
        
        // Create usershare
        $cmd = "net usershare add " . escapeshellarg($name) . " " . escapeshellarg($path);
        if (!empty($comment)) {
            $cmd .= " " . escapeshellarg($comment);
        }
        $cmd .= " Everyone:F";
        if ($guest) {
            $cmd .= " guest_ok=y";
        }
        
        if (!$this->exec($cmd, $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'SMB Share Created',
            'message' => "SMB share created: $name at $path",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function removeShare($name) {
        $name = escapeshellarg($name);
        
        if (!$this->exec("net usershare delete $name", $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'SMB Share Removed',
            'message' => "SMB share removed: $name",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function listUsers() {
        if (!$this->exec('pdbedit -L', $output, $code)) {
            return ['users' => []];
        }
        
        $users = [];
        foreach ($output as $line) {
            if (empty($line)) continue;
            
            // Parse: username:uid:Full Name
            $parts = explode(':', $line);
            if (count($parts) >= 3) {
                $users[] = [
                    'username' => $parts[0],
                    'uid' => $parts[1],
                    'fullname' => $parts[2]
                ];
            }
        }
        
        return ['users' => $users];
    }
    
    public function addUser($username, $password) {
        $username = escapeshellarg($username);
        
        // Check if user exists
        if (!$this->exec("id $username", $output, $code)) {
            return ['success' => false, 'error' => 'System user does not exist'];
        }
        
        // Add to Samba
        $cmd = "printf '$password\\n$password\\n' | smbpasswd -a -s $username";
        if (!$this->exec($cmd, $output, $code)) {
            return ['success' => false, 'error' => 'Failed to add Samba user'];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'SMB User Added',
            'message' => "Samba user added: $username",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function removeUser($username) {
        $username = escapeshellarg($username);
        
        if (!$this->exec("smbpasswd -x $username", $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'SMB User Removed',
            'message' => "Samba user removed: $username",
            'category' => 'shares'
        ]);
        
        return ['success' => true];
    }
    
    public function getStatus() {
        $status = [];
        
        // Check if Samba is running
        $this->exec('systemctl is-active smbd', $output, $code);
        $status['smbd'] = $code === 0 ? 'active' : 'inactive';
        
        $this->exec('systemctl is-active nmbd', $output, $code);
        $status['nmbd'] = $code === 0 ? 'active' : 'inactive';
        
        return ['status' => $status];
    }
    
    public function restartService() {
        $success = true;
        
        if (!$this->exec('systemctl restart smbd', $output, $code)) {
            $success = false;
        }
        
        if (!$this->exec('systemctl restart nmbd', $output, $code)) {
            $success = false;
        }
        
        if ($success) {
            $this->notifications->create([
                'type' => 'info',
                'title' => 'Samba Restarted',
                'message' => 'Samba services restarted',
                'category' => 'shares'
            ]);
            return ['success' => true];
        }
        
        return ['success' => false, 'error' => 'Failed to restart Samba'];
    }
}

$smb = new SMBManager();
$action = $_GET['action'] ?? $_POST['action'] ?? 'list';

switch ($action) {
    case 'list':
        echo json_encode($smb->listShares());
        break;
    
    case 'details':
        echo json_encode($smb->getShareDetails($_GET['name']));
        break;
    
    case 'add':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($smb->addShare($data));
        break;
    
    case 'remove':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($smb->removeShare($data['name']));
        break;
    
    case 'users':
        echo json_encode($smb->listUsers());
        break;
    
    case 'add_user':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($smb->addUser($data['username'], $data['password']));
        break;
    
    case 'remove_user':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($smb->removeUser($data['username']));
        break;
    
    case 'status':
        echo json_encode($smb->getStatus());
        break;
    
    case 'restart':
        echo json_encode($smb->restartService());
        break;
    
    default:
        echo json_encode(['error' => 'Unknown action']);
}
