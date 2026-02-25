<?php
/**
 * D-PlaneOS v1.14.0 - System Stats API
 * Provides real-time system statistics for dashboard
 */

header('Content-Type: application/json');
header('Access-Control-Allow-Origin: *');

$action = $_GET['action'] ?? 'stats';

switch ($action) {
    case 'stats':
        echo json_encode(getSystemStats());
        break;
    default:
        echo json_encode(['error' => 'Unknown action']);
}

function getSystemStats() {
    return [
        'cpu' => getCPUUsage(),
        'memory' => getMemoryUsage(),
        'disk' => getDiskUsage(),
        'network' => getNetworkStats(),
        'uptime' => getUptime()
    ];
}

function getCPUUsage() {
    $load = sys_getloadavg();
    $cpu_count = (int)shell_exec('nproc');
    return round(($load[0] / $cpu_count) * 100, 2);
}

function getMemoryUsage() {
    $free = shell_exec('free -b');
    $lines = explode("\n", trim($free));
    $mem = preg_split('/\s+/', $lines[1]);
    
    $total = (int)$mem[1];
    $used = (int)$mem[2];
    
    return round(($used / $total) * 100, 2);
}

function getDiskUsage() {
    $df = shell_exec('df / | tail -1');
    $parts = preg_split('/\s+/', $df);
    $percent = (int)str_replace('%', '', $parts[4]);
    
    return $percent;
}

function getNetworkStats() {
    // Read /proc/net/dev for network stats
    $data = file_get_contents('/proc/net/dev');
    $lines = explode("\n", $data);
    
    $totalIn = 0;
    $totalOut = 0;
    
    foreach ($lines as $line) {
        if (strpos($line, ':') === false) continue;
        if (strpos($line, 'lo:') !== false) continue; // Skip loopback
        
        $parts = preg_split('/\s+/', trim($line));
        $totalIn += (int)$parts[1];
        $totalOut += (int)$parts[9];
    }
    
    return [
        'in' => $totalIn,
        'out' => $totalOut
    ];
}

function getUptime() {
    return (int)shell_exec('cat /proc/uptime | cut -d" " -f1');
}
