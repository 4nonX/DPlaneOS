<?php
/**
 * D-PlaneOS v1.14.0 - User & Group Management
 * Real system commands for user/group management
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

require_once __DIR__ . '/notifications.php';

class UserManager {
    private $notifications;
    
    public function __construct() {
        $this->notifications = new NotificationSystem();
    }
    
    private function exec($command, &$output = null, &$returnCode = null) {
        exec($command . ' 2>&1', $output, $returnCode);
        error_log("[Users] $command => $returnCode");
        return $returnCode === 0;
    }
    
    public function listUsers() {
        // Get regular users (UID >= 1000)
        if (!$this->exec("getent passwd | awk -F: '\$3 >= 1000 && \$3 < 65534'", $output, $code)) {
            return ['users' => []];
        }
        
        $users = [];
        foreach ($output as $line) {
            $parts = explode(':', $line);
            if (count($parts) >= 7) {
                $users[] = [
                    'username' => $parts[0],
                    'uid' => $parts[2],
                    'gid' => $parts[3],
                    'fullname' => $parts[4],
                    'home' => $parts[5],
                    'shell' => $parts[6]
                ];
            }
        }
        
        return ['users' => $users];
    }
    
    public function addUser($data) {
        $username = $data['username'];
        $fullname = $data['fullname'] ?? '';
        $password = $data['password'] ?? '';
        $shell = $data['shell'] ?? '/bin/bash';
        $createHome = $data['createHome'] ?? true;
        
        // Validate username
        if (!preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $username)) {
            return ['success' => false, 'error' => 'Invalid username'];
        }
        
        // Build useradd command
        $cmd = 'useradd';
        if ($createHome) $cmd .= ' -m';
        if (!empty($shell)) $cmd .= ' -s ' . escapeshellarg($shell);
        if (!empty($fullname)) $cmd .= ' -c ' . escapeshellarg($fullname);
        $cmd .= ' ' . escapeshellarg($username);
        
        if (!$this->exec($cmd, $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        // Set password if provided
        if (!empty($password)) {
            $passwdCmd = "echo " . escapeshellarg("$username:$password") . " | chpasswd";
            $this->exec($passwdCmd, $passOutput, $passCode);
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'User Created',
            'message' => "User $username created",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function removeUser($username, $removeHome = false) {
        $cmd = 'userdel';
        if ($removeHome) $cmd .= ' -r';
        $cmd .= ' ' . escapeshellarg($username);
        
        if (!$this->exec($cmd, $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'warning',
            'title' => 'User Removed',
            'message' => "User $username removed",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function changePassword($username, $password) {
        $cmd = "echo " . escapeshellarg("$username:$password") . " | chpasswd";
        
        if (!$this->exec($cmd, $output, $code)) {
            return ['success' => false, 'error' => 'Failed to change password'];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'Password Changed',
            'message' => "Password changed for $username",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function listGroups() {
        if (!$this->exec("getent group | awk -F: '\$3 >= 1000 && \$3 < 65534'", $output, $code)) {
            return ['groups' => []];
        }
        
        $groups = [];
        foreach ($output as $line) {
            $parts = explode(':', $line);
            if (count($parts) >= 4) {
                $groups[] = [
                    'name' => $parts[0],
                    'gid' => $parts[2],
                    'members' => !empty($parts[3]) ? explode(',', $parts[3]) : []
                ];
            }
        }
        
        return ['groups' => $groups];
    }
    
    public function addGroup($name) {
        if (!preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $name)) {
            return ['success' => false, 'error' => 'Invalid group name'];
        }
        
        if (!$this->exec('groupadd ' . escapeshellarg($name), $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'Group Created',
            'message' => "Group $name created",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function removeGroup($name) {
        if (!$this->exec('groupdel ' . escapeshellarg($name), $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        $this->notifications->create([
            'type' => 'warning',
            'title' => 'Group Removed',
            'message' => "Group $name removed",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function addUserToGroup($username, $groupname) {
        if (!$this->exec("usermod -aG " . escapeshellarg($groupname) . " " . escapeshellarg($username), $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        return ['success' => true];
    }
    
    public function removeUserFromGroup($username, $groupname) {
        if (!$this->exec("gpasswd -d " . escapeshellarg($username) . " " . escapeshellarg($groupname), $output, $code)) {
            return ['success' => false, 'error' => implode("\n", $output)];
        }
        
        return ['success' => true];
    }
}

$users = new UserManager();
$action = $_GET['action'] ?? $_POST['action'] ?? 'list';

switch ($action) {
    case 'list':
        echo json_encode($users->listUsers());
        break;
    
    case 'add':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->addUser($data));
        break;
    
    case 'remove':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->removeUser($data['username'], $data['removeHome'] ?? false));
        break;
    
    case 'change_password':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->changePassword($data['username'], $data['password']));
        break;
    
    case 'list_groups':
        echo json_encode($users->listGroups());
        break;
    
    case 'add_group':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->addGroup($data['name']));
        break;
    
    case 'remove_group':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->removeGroup($data['name']));
        break;
    
    case 'add_to_group':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->addUserToGroup($data['username'], $data['group']));
        break;
    
    case 'remove_from_group':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($users->removeUserFromGroup($data['username'], $data['group']));
        break;
    
    default:
        echo json_encode(['error' => 'Unknown action']);
}
