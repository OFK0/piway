<?php
// Shared by the web pages and the cleanup script: the database, the
// attachments directory and a few helpers.

declare(strict_types=1);

const ATTACHMENTS = '/data/attachments';
const MAX_ATTACHMENT = 5 << 20;

function log_line(string $msg, array $fields = []): void
{
    // stdout is the container's log, which piway shows.
    file_put_contents('php://stdout', json_encode(['time' => gmdate('c'), 'msg' => $msg] + $fields) . "\n");
}

// db connects with DATABASE_URL, which piway sets for run.databases, and
// makes the table on first use.
function db(): PDO
{
    static $pdo = null;
    if ($pdo !== null) {
        return $pdo;
    }
    $u = parse_url((string) getenv('DATABASE_URL'));
    if (!isset($u['host'], $u['user'], $u['pass'], $u['path'])) {
        throw new RuntimeException('DATABASE_URL is not set: is mariadb in run.databases?');
    }
    $dsn = sprintf('mysql:host=%s;port=%d;dbname=%s;charset=utf8mb4', $u['host'], $u['port'] ?? 3306, ltrim($u['path'], '/'));
    $pdo = new PDO($dsn, urldecode($u['user']), urldecode($u['pass']), [
        PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
        PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
    ]);
    // Additive only, so it is safe while an older version still runs
    // against the same database during a deploy.
    $pdo->exec('CREATE TABLE IF NOT EXISTS notes (
        id INT AUTO_INCREMENT PRIMARY KEY,
        title VARCHAR(200) NOT NULL,
        body TEXT NOT NULL,
        attachment VARCHAR(100) NULL,
        attachment_name VARCHAR(200) NULL,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
    ) CHARACTER SET utf8mb4');
    return $pdo;
}

function h(?string $s): string
{
    return htmlspecialchars((string) $s, ENT_QUOTES, 'UTF-8');
}

function delete_attachment(?string $file): void
{
    if ($file !== null && $file !== '' && preg_match('/^[a-f0-9]{32}$/', $file)) {
        @unlink(ATTACHMENTS . '/' . $file);
    }
}
