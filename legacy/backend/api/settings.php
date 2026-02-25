<?php
/**
 * D-PlaneOS v1.14.0 - System Settings & Management
 * Real systemctl commands for service/system management
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

require_once __DIR__ . '/notifications.php';

class SettingsManager {
    private $notifications;
    
    public function __construct() {
        $this->notifications = new NotificationSystem();
    }
    
    private function exec($cmd, &$out = null, &$code = null) {
        exec($cmd . ' 2>&1', $out, $code);
        error_log("[Settings] $cmd => $code");
        return $code === 0;
    }
    
    public function getSystemInfo() {
        $info = [];
        
        // Hostname
        $this->exec('hostname', $out, $code);
        $info['hostname'] = trim($out[0] ?? 'unknown');
        
        // Uptime
        $this->exec('uptime -p', $out, $code);
        $info['uptime'] = trim($out[0] ?? 'unknown');
        
        // Kernel
        $this->exec('uname -r', $out, $code);
        $info['kernel'] = trim($out[0] ?? 'unknown');
        
        // OS
        if (file_exists('/etc/os-release')) {
            $osRelease = parse_ini_file('/etc/os-release');
            $info['os'] = $osRelease['PRETTY_NAME'] ?? 'Linux';
        }
        
        // Timezone
        $this->exec('timedatectl show -p Timezone --value', $out, $code);
        $info['timezone'] = trim($out[0] ?? 'UTC');
        
        return $info;
    }
    
    public function setTimezone($tz) {
        if (!$this->exec("timedatectl set-timezone " . escapeshellarg($tz), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'Timezone Changed',
            'message' => "Timezone set to $tz",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function listServices() {
        if (!$this->exec("systemctl list-units --type=service --no-pager --no-legend", $out, $code)) {
            return ['services' => []];
        }
        
        $services = [];
        foreach ($out as $line) {
            if (empty($line)) continue;
            $parts = preg_split('/\s+/', trim($line), 5);
            if (count($parts) >= 4) {
                $services[] = [
                    'name' => $parts[0],
                    'load' => $parts[1],
                    'active' => $parts[2],
                    'sub' => $parts[3],
                    'description' => $parts[4] ?? ''
                ];
            }
        }
        
        return ['services' => $services];
    }
    
    public function getServiceStatus($service) {
        $this->exec("systemctl status " . escapeshellarg($service), $out, $code);
        return ['status' => implode("\n", $out), 'active' => $code === 0];
    }
    
    public function startService($service) {
        if (!$this->exec("systemctl start " . escapeshellarg($service), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'Service Started',
            'message' => "Service $service started",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function stopService($service) {
        if (!$this->exec("systemctl stop " . escapeshellarg($service), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'Service Stopped',
            'message' => "Service $service stopped",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function restartService($service) {
        if (!$this->exec("systemctl restart " . escapeshellarg($service), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        
        $this->notifications->create([
            'type' => 'info',
            'title' => 'Service Restarted',
            'message' => "Service $service restarted",
            'category' => 'system'
        ]);
        
        return ['success' => true];
    }
    
    public function enableService($service) {
        if (!$this->exec("systemctl enable " . escapeshellarg($service), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        return ['success' => true];
    }
    
    public function disableService($service) {
        if (!$this->exec("systemctl disable " . escapeshellarg($service), $out, $code)) {
            return ['success' => false, 'error' => implode("\n", $out)];
        }
        return ['success' => true];
    }
    
    public function getLogs($service = null, $lines = 100) {
        $cmd = "journalctl -n " . (int)$lines . " --no-pager";
        if ($service) {
            $cmd .= " -u " . escapeshellarg($service);
        }
        
        if (!$this->exec($cmd, $out, $code)) {
            return ['logs' => 'Failed to retrieve logs'];
        }
        
        return ['logs' => implode("\n", $out)];
    }
    
    public function checkUpdates() {
        // Check for apt updates
        $this->exec("apt-get update > /dev/null 2>&1 && apt list --upgradable 2>/dev/null", $out, $code);
        
        $updates = 0;
        foreach ($out as $line) {
            if (strpos($line, 'upgradable') !== false) {
                $updates++;
            }
        }
        
        return ['available' => $updates];
    }
    
    public function installUpdates() {
        if (!$this->exec("apt-get upgrade -y", $out, $code)) {
            return ['success' => false, 'error' => 'Update failed'];
        }
        
        $this->notifications->create([
            'type' => 'success',
            'title' => 'System Updated',
            'message' => 'System packages updated',
            'category' => 'system'
        ]);
        
        return ['success' => true, 'output' => implode("\n", $out)];
    }
    
    public function reboot() {
        $this->notifications->create([
            'type' => 'warning',
            'title' => 'System Rebooting',
            'message' => 'System is rebooting...',
            'category' => 'system'
        ]);
        
        $this->exec("shutdown -r +1 'System reboot initiated from D-PlaneOS'", $out, $code);
        return ['success' => true, 'message' => 'System will reboot in 1 minute'];
    }
    
    public function shutdown() {
        $this->notifications->create([
            'type' => 'critical',
            'title' => 'System Shutting Down',
            'message' => 'System is shutting down...',
            'category' => 'system'
        ]);
        
        $this->exec("shutdown -h +1 'System shutdown initiated from D-PlaneOS'", $out, $code);
        return ['success' => true, 'message' => 'System will shutdown in 1 minute'];
    }
}

$settings = new SettingsManager();
$action = $_GET['action'] ?? $_POST['action'] ?? 'info';

switch ($action) {
    case 'info':
        echo json_encode($settings->getSystemInfo());
        break;
    
    case 'set_timezone':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->setTimezone($data['timezone']));
        break;
    
    case 'services':
        echo json_encode($settings->listServices());
        break;
    
    case 'service_status':
        echo json_encode($settings->getServiceStatus($_GET['service']));
        break;
    
    case 'start_service':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->startService($data['service']));
        break;
    
    case 'stop_service':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->stopService($data['service']));
        break;
    
    case 'restart_service':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->restartService($data['service']));
        break;
    
    case 'enable_service':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->enableService($data['service']));
        break;
    
    case 'disable_service':
        $data = json_decode(file_get_contents('php://input'), true);
        echo json_encode($settings->disableService($data['service']));
        break;
    
    case 'logs':
        echo json_encode($settings->getLogs($_GET['service'] ?? null, $_GET['lines'] ?? 100));
        break;
    
    case 'check_updates':
        echo json_encode($settings->checkUpdates());
        break;
    
    case 'install_updates':
        echo json_encode($settings->installUpdates());
        break;
    
    case 'reboot':
        echo json_encode($settings->reboot());
        break;
    
    case 'shutdown':
        echo json_encode($settings->shutdown());
        break;
    
    default:
        echo json_encode(['error' => 'Unknown action']);
}
