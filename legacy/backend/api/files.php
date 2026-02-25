<?php
/**
 * D-PlaneOS v1.14.0 - File Browser API
 * Restricted to /mnt/ subtree (ZFS pool mounts)
 */

// ─── No upload size limit (also enforced via .htaccess in this directory) ───
ini_set('upload_max_filesize', '512G');
ini_set('post_max_size',       '512G');
ini_set('max_execution_time',  '0');
ini_set('max_input_time',      '-1');

// ─── download / preview bypass the JSON content-type ───
$action = $_GET['action'] ?? $_POST['action'] ?? 'list';
if ($action !== 'download' && $action !== 'preview') {
    header('Content-Type: application/json');
}
header('Access-Control-Allow-Origin: *');

// ─── Security: every path must resolve under BASE ───
define('BASE', '/mnt');

function safePath(string $raw): string|false {
    $real = realpath($raw);
    if ($real === false) {
        // target may not exist yet (mkdir / create / upload dest) — validate via parent
        $parent = realpath(dirname($raw));
        if ($parent === false) return false;
        $candidate = $parent . '/' . basename($raw);
        if (strpos($candidate, BASE . '/') !== 0 && $candidate !== BASE) return false;
        return $candidate;
    }
    if (strpos($real, BASE . '/') !== 0 && $real !== BASE) return false;
    return $real;
}

function humanSize(int $bytes): string {
    if ($bytes === 0) return '0 B';
    $units = ['B','KB','MB','GB','TB'];
    $i = min((int)(log($bytes, 1024)), count($units) - 1);
    return round($bytes / pow(1024, $i), 2) . ' ' . $units[$i];
}

function mimeIcon(string $ext): string {
    $map = [
        'jpg'=>'🖼️','jpeg'=>'🖼️','png'=>'🖼️','gif'=>'🖼️','svg'=>'🖼️','webp'=>'🖼️','bmp'=>'🖼️',
        'mp4'=>'🎬','avi'=>'🎬','mkv'=>'🎬','mov'=>'🎬','webm'=>'🎬',
        'mp3'=>'🎵','flac'=>'🎵','wav'=>'🎵','ogg'=>'🎵','m4a'=>'🎵',
        'pdf'=>'📄','doc'=>'📄','docx'=>'📄','txt'=>'📝','md'=>'📝',
        'zip'=>'🗜️','tar'=>'🗜️','gz'=>'🗜️','7z'=>'🗜️','rar'=>'🗜️',
        'php'=>'⚙️','py'=>'⚙️','js'=>'⚙️','ts'=>'⚙️','sh'=>'⚙️',
        'yml'=>'⚙️','yaml'=>'⚙️','json'=>'⚙️','conf'=>'⚙️','cfg'=>'⚙️','ini'=>'⚙️','env'=>'⚙️',
        'css'=>'🎨','html'=>'🌐','htm'=>'🌐','xml'=>'📋',
        'dockerfile'=>'📦','sql'=>'🗄️','db'=>'🗄️','csv'=>'📊','tsv'=>'📊','log'=>'📋',
        'iso'=>'💿','img'=>'💿',
    ];
    return $map[strtolower($ext)] ?? '📄';
}

function getMimeType(string $path): string {
    if (function_exists('mime_content_type')) {
        $m = @mime_content_type($path);
        if ($m && $m !== 'application/octet-stream') return $m;
    }
    $ext = strtolower(pathinfo($path, PATHINFO_EXTENSION));
    $map = [
        'jpg'=>'image/jpeg','jpeg'=>'image/jpeg','png'=>'image/png','gif'=>'image/gif',
        'webp'=>'image/webp','svg'=>'image/svg+xml','bmp'=>'image/bmp',
        'mp4'=>'video/mp4','webm'=>'video/webm','ogv'=>'video/ogg',
        'mp3'=>'audio/mpeg','wav'=>'audio/wav','ogg'=>'audio/ogg','flac'=>'audio/flac','m4a'=>'audio/mp4',
        'pdf'=>'application/pdf','json'=>'application/json','xml'=>'application/xml',
        'html'=>'text/html','htm'=>'text/html','css'=>'text/css','js'=>'application/javascript',
        'csv'=>'text/csv','txt'=>'text/plain','md'=>'text/markdown',
        'yml'=>'text/yaml','yaml'=>'text/yaml','sh'=>'text/x-shellscript','php'=>'text/x-php',
        'py'=>'text/x-python','sql'=>'text/x-sql',
    ];
    return $map[$ext] ?? 'application/octet-stream';
}

switch ($action) {

    // ─── LIST ───
    case 'list': {
        $path = safePath($_GET['path'] ?? BASE);
        if ($path === false || !is_dir($path)) {
            echo json_encode(['error' => 'Invalid or inaccessible path']);
            break;
        }
        $entries = scandir($path);
        $dirs = $files = [];
        foreach ($entries as $name) {
            if ($name === '.' || $name === '..') continue;
            $full = $path . '/' . $name;
            $stat = @stat($full);
            if (!$stat) continue;
            if (is_dir($full)) {
                $dirs[] = ['name' => $name, 'mtime' => date('Y-m-d H:i', $stat['mtime'])];
            } else {
                $ext = pathinfo($name, PATHINFO_EXTENSION);
                $files[] = [
                    'name'  => $name,
                    'size'  => humanSize($stat['size']),
                    'bytes' => $stat['size'],
                    'mtime' => date('Y-m-d H:i', $stat['mtime']),
                    'icon'  => mimeIcon($ext),
                    'ext'   => $ext,
                ];
            }
        }
        usort($dirs,  fn($a,$b) => strcmp($a['name'], $b['name']));
        usort($files, fn($a,$b) => strcmp($a['name'], $b['name']));
        echo json_encode(['path' => $path, 'dirs' => $dirs, 'files' => $files]);
        break;
    }

    // ─── MKDIR ───
    case 'mkdir': {
        $data   = json_decode(file_get_contents('php://input'), true);
        $parent = safePath($data['path'] ?? '');
        $name   = basename($data['name'] ?? '');
        if ($parent === false || !is_dir($parent) || empty($name)) {
            echo json_encode(['success' => false, 'error' => 'Invalid path or name']); break;
        }
        $target = $parent . '/' . $name;
        if (is_dir($target)) { echo json_encode(['success' => false, 'error' => 'Directory already exists']); break; }
        echo json_encode(['success' => @mkdir($target, 0775), 'error' => 'Failed to create directory']);
        break;
    }

    // ─── CREATE (new empty file) ───
    case 'create': {
        $data   = json_decode(file_get_contents('php://input'), true);
        $parent = safePath($data['path'] ?? '');
        $name   = basename($data['name'] ?? '');
        if ($parent === false || !is_dir($parent) || empty($name)) {
            echo json_encode(['success' => false, 'error' => 'Invalid path or name']); break;
        }
        $target = $parent . '/' . $name;
        if (file_exists($target)) { echo json_encode(['success' => false, 'error' => 'Already exists']); break; }
        echo json_encode(['success' => (file_put_contents($target, '') !== false), 'error' => 'Failed to create file']);
        break;
    }

    // ─── DELETE ───
    case 'delete': {
        $data   = json_decode(file_get_contents('php://input'), true);
        $target = safePath($data['path'] ?? '');
        if ($target === false || $target === BASE) {
            echo json_encode(['success' => false, 'error' => 'Cannot delete base path']); break;
        }
        if (is_file($target)) {
            echo json_encode(['success' => @unlink($target), 'error' => 'Failed to delete file']);
        } elseif (is_dir($target)) {
            $contents = array_diff(scandir($target), ['.','..']);
            if (!empty($contents)) { echo json_encode(['success' => false, 'error' => 'Directory not empty']); break; }
            echo json_encode(['success' => @rmdir($target), 'error' => 'Failed to remove directory']);
        } else {
            echo json_encode(['success' => false, 'error' => 'Path not found']);
        }
        break;
    }

    // ─── RENAME ───
    case 'rename': {
        $data    = json_decode(file_get_contents('php://input'), true);
        $oldPath = safePath($data['oldPath'] ?? '');
        $dir     = dirname($oldPath ?: '');
        $newName = basename($data['newName'] ?? '');
        $newPath = $dir . '/' . $newName;
        if ($oldPath === false || empty($newName) || safePath($newPath) === false) {
            echo json_encode(['success' => false, 'error' => 'Invalid path']); break;
        }
        if (file_exists($newPath)) { echo json_encode(['success' => false, 'error' => 'Name already exists']); break; }
        echo json_encode(['success' => @rename($oldPath, $newPath), 'error' => 'Rename failed']);
        break;
    }

    // ─── READ (text content for viewer / editor — capped at 10 MB) ───
    case 'read': {
        $path = safePath($_GET['path'] ?? '');
        if ($path === false || !is_file($path)) {
            echo json_encode(['error' => 'File not found']); break;
        }
        $size = filesize($path);
        if ($size > 10 * 1024 * 1024) {
            echo json_encode(['error' => 'File too large for in-browser editing (' . humanSize($size) . '). Download and edit locally.']);
            break;
        }
        $content = file_get_contents($path);
        if ($content === false) { echo json_encode(['error' => 'Failed to read file']); break; }
        // Null bytes → binary
        if (strpos($content, "\0") !== false) {
            echo json_encode(['error' => 'Binary file — cannot edit as text. Use download.']); break;
        }
        echo json_encode(['content' => $content, 'size' => humanSize($size), 'ext' => pathinfo($path, PATHINFO_EXTENSION)]);
        break;
    }

    // ─── SAVE (write edited text back) ───
    case 'save': {
        $data    = json_decode(file_get_contents('php://input'), true);
        $path    = safePath($data['path'] ?? '');
        $content = $data['content'] ?? '';
        if ($path === false || !is_file($path)) {
            echo json_encode(['success' => false, 'error' => 'File not found']); break;
        }
        echo json_encode(['success' => (file_put_contents($path, $content) !== false), 'error' => 'Failed to write file']);
        break;
    }

    // ─── UPLOAD ───
    case 'upload': {
        $dir = safePath($_POST['dir'] ?? '');
        if ($dir === false || !is_dir($dir)) {
            echo json_encode(['success' => false, 'error' => 'Invalid destination']); break;
        }
        if (!isset($_FILES['file']) || $_FILES['file']['error'] !== UPLOAD_ERR_OK) {
            $errs = [
                UPLOAD_ERR_INI_SIZE   => 'Exceeds upload_max_filesize',
                UPLOAD_ERR_FORM_SIZE  => 'Exceeds MAX_FILE_SIZE',
                UPLOAD_ERR_PARTIAL    => 'Upload interrupted',
                UPLOAD_ERR_NO_FILE    => 'No file selected',
                UPLOAD_ERR_NO_TMP_DIR => 'No tmp directory',
                UPLOAD_ERR_CANT_WRITE => 'Cannot write to disk',
            ];
            $code = $_FILES['file']['error'] ?? -1;
            echo json_encode(['success' => false, 'error' => $errs[$code] ?? 'Upload failed']);
            break;
        }
        $name   = basename($_FILES['file']['name']);
        $target = $dir . '/' . $name;
        if (move_uploaded_file($_FILES['file']['tmp_name'], $target)) {
            echo json_encode(['success' => true, 'name' => $name]);
        } else {
            echo json_encode(['success' => false, 'error' => 'Failed to save file']);
        }
        break;
    }

    // ─── DOWNLOAD ───
    case 'download': {
        $path = safePath($_GET['path'] ?? '');
        if ($path === false || !is_file($path)) {
            header('Content-Type: application/json');
            echo json_encode(['error' => 'File not found']); break;
        }
        $name = basename($path);
        header('Content-Type: application/octet-stream');
        header("Content-Disposition: attachment; filename=\"$name\"");
        header('Content-Length: ' . filesize($path));
        header('Cache-Control: no-store');
        readfile($path);
        exit;
    }

    // ─── PREVIEW (serve inline with correct MIME — for images / video / audio) ───
    case 'preview': {
        $path = safePath($_GET['path'] ?? '');
        if ($path === false || !is_file($path)) {
            header('Content-Type: application/json');
            echo json_encode(['error' => 'File not found']); break;
        }
        $name = basename($path);
        header('Content-Type: ' . getMimeType($path));
        header("Content-Disposition: inline; filename=\"$name\"");
        header('Content-Length: ' . filesize($path));
        header('Cache-Control: public, max-age=3600');
        readfile($path);
        exit;
    }

    default:
        echo json_encode(['error' => 'Unknown action']);
}
