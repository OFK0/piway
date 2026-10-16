<?php
// notes: an example app for piway. Notes in MariaDB, attachments on the
// data volume, sessions in Redis. Every request comes here (Apache's
// FallbackResource).

declare(strict_types=1);

require __DIR__ . '/../src/lib.php';

$method = $_SERVER['REQUEST_METHOD'];
$path = rtrim(parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH) ?: '/', '/') ?: '/';

// The contract endpoints come first: they need no session.
if ($path === '/healthz') {
    try {
        db()->query('SELECT 1');
        echo "ok\n";
    } catch (Throwable $e) {
        http_response_code(503);
        echo "database: {$e->getMessage()}\n";
    }
    exit;
}
if ($path === '/api/info') {
    $secret = (string) getenv('NOTES_TOKEN');
    header('Content-Type: application/json');
    echo json_encode([
        'app' => 'notes',
        'language' => 'PHP',
        'runtime' => 'PHP ' . PHP_VERSION . ', ' . explode(' ', (string) ($_SERVER['SERVER_SOFTWARE'] ?? 'Apache'))[0],
        'env' => ['GREETING' => (string) getenv('GREETING')],
        'secret' => ['name' => 'NOTES_TOKEN', 'set' => $secret !== '', 'length' => strlen($secret),
            'sha256_prefix' => substr(hash('sha256', $secret), 0, 8)],
        'sessions' => ini_get('session.save_handler'),
    ]);
    exit;
}

// Sessions live in Redis, under the app's prefix, so every instance (and
// the next version, during a deploy) sees them.
$r = parse_url((string) getenv('REDIS_URL'));
if (isset($r['host'])) {
    ini_set('session.save_handler', 'redis');
    ini_set('session.save_path', sprintf('tcp://%s:%d?auth[]=%s&auth[]=%s&prefix=%s',
        $r['host'], $r['port'] ?? 6379, rawurlencode(urldecode($r['user'] ?? '')), rawurlencode(urldecode($r['pass'] ?? '')),
        rawurlencode((string) getenv('REDIS_PREFIX') . 'sess:')));
}
session_start(['cookie_httponly' => true, 'cookie_samesite' => 'Lax']);
$_SESSION['csrf'] ??= bin2hex(random_bytes(16));
$_SESSION['views'] = ($_SESSION['views'] ?? 0) + 1;

function flash(string $msg): void { $_SESSION['flash'] = $msg; }
function redirect(string $to): never { header('Location: ' . $to, true, 303); exit; }
function check_csrf(): void
{
    if (!hash_equals($_SESSION['csrf'], (string) ($_POST['csrf'] ?? ''))) {
        http_response_code(400);
        exit('stale form; go back and try again');
    }
}

// save_attachment stores an uploaded file and returns its id, or null.
function save_attachment(): ?array
{
    $f = $_FILES['attachment'] ?? null;
    if ($f === null || $f['error'] === UPLOAD_ERR_NO_FILE) {
        return null;
    }
    if ($f['error'] !== UPLOAD_ERR_OK || $f['size'] > MAX_ATTACHMENT) {
        throw new RuntimeException('attachments are up to ' . (MAX_ATTACHMENT >> 20) . ' MB');
    }
    @mkdir(ATTACHMENTS, 0o755, true);
    $id = bin2hex(random_bytes(16));
    if (!move_uploaded_file($f['tmp_name'], ATTACHMENTS . '/' . $id)) {
        throw new RuntimeException('could not store the attachment');
    }
    return [$id, mb_substr(basename($f['name']), 0, 200)];
}

function page(string $title, string $body): never
{
    $flash = $_SESSION['flash'] ?? '';
    unset($_SESSION['flash']);
    $views = (int) $_SESSION['views'];
    echo <<<HTML
    <!doctype html><html lang="en"><head><meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1"><title>{$title} · notes</title>
    <style>
      :root { color-scheme: light dark; font: 16px/1.5 system-ui, sans-serif; }
      body { max-width: 40rem; margin: 2rem auto; padding: 0 1rem; }
      .muted { color: GrayText; font-size: .875rem; } .flash { border-left: 3px solid; padding-left: .5rem; }
      input[type=text], textarea { width: 100%; box-sizing: border-box; } textarea { min-height: 8rem; }
      ul { padding-left: 1.25rem; } form.inline { display: inline; }
    </style></head><body>
    <h1><a href="/">notes</a></h1>
    HTML;
    if ($flash !== '') {
        echo '<p class="flash" role="status">' . h($flash) . '</p>';
    }
    echo $body;
    echo "<p class=\"muted\">{$views} pages this session (kept in Redis).</p></body></html>";
    exit;
}

function note_form(array $n, string $action): string
{
    $csrf = h($_SESSION['csrf']);
    $title = h($n['title']);
    $body = h($n['body']);
    $att = $n['attachment_name'] ? '<p class="muted">Attached: ' . h($n['attachment_name']) . ' (a new file replaces it)</p>' : '';
    return <<<HTML
    <form method="post" action="{$action}" enctype="multipart/form-data">
      <input type="hidden" name="csrf" value="{$csrf}">
      <p><label>Title<br><input type="text" name="title" maxlength="200" required value="{$title}"></label></p>
      <p><label>Note<br><textarea name="body">{$body}</textarea></label></p>
      <p><label>Attachment <input type="file" name="attachment"></label></p>{$att}
      <p><button>Save</button></p>
    </form>
    HTML;
}
try {
    if ($method === 'GET' && $path === '/') {
        $rows = db()->query('SELECT id, title, attachment_name, updated_at FROM notes ORDER BY updated_at DESC LIMIT 200')->fetchAll();
        $items = '';
        foreach ($rows as $n) {
            $clip = $n['attachment_name'] ? ' 📎' : '';
            $items .= '<li><a href="/notes/' . $n['id'] . '">' . h($n['title']) . "</a>{$clip} <span class=\"muted\">" . h($n['updated_at']) . '</span></li>';
        }
        page('Notes', '<p><a href="/notes/new">New note</a></p>' . ($items ? "<ul>{$items}</ul>" : '<p class="muted">No notes yet.</p>'));
    }
    if ($method === 'GET' && $path === '/notes/new') {
        page('New note', note_form(['title' => '', 'body' => '', 'attachment_name' => null], '/notes'));
    }
    if ($method === 'POST' && $path === '/notes') {
        check_csrf();
        [$file, $name] = save_attachment() ?? [null, null];
        db()->prepare('INSERT INTO notes (title, body, attachment, attachment_name) VALUES (?, ?, ?, ?)')
            ->execute([mb_substr(trim($_POST['title'] ?? ''), 0, 200) ?: 'Untitled', $_POST['body'] ?? '', $file, $name]);
        $id = (int) db()->lastInsertId();
        log_line('note created', ['id' => $id, 'attachment' => $file !== null]);
        flash('Saved.');
        redirect("/notes/{$id}");
    }
    if (preg_match('#^/notes/(\d+)(/edit|/delete)?$#', $path, $m)) {
        $st = db()->prepare('SELECT * FROM notes WHERE id = ?');
        $st->execute([(int) $m[1]]);
        $n = $st->fetch();
        if (!$n) {
            http_response_code(404);
            page('Not found', '<p>No such note.</p>');
        }
        $action = $m[2] ?? '';
        if ($method === 'GET' && $action === '') {
            $att = $n['attachment'] ? '<p>📎 <a href="/files/' . $n['attachment'] . '">' . h($n['attachment_name']) . '</a></p>' : '';
            $csrf = h($_SESSION['csrf']);
            page(h($n['title']), '<h2>' . h($n['title']) . '</h2><p style="white-space: pre-wrap">' . h($n['body']) . "</p>{$att}"
                . "<p><a href=\"/notes/{$n['id']}/edit\">Edit</a> · <form class=\"inline\" method=\"post\" action=\"/notes/{$n['id']}/delete\">"
                . "<input type=\"hidden\" name=\"csrf\" value=\"{$csrf}\"><button>Delete</button></form></p>");
        }
        if ($method === 'GET' && $action === '/edit') {
            page('Edit', note_form($n, "/notes/{$n['id']}"));
        }
        if ($method === 'POST' && $action === '') {
            check_csrf();
            $new = save_attachment();
            if ($new !== null) {
                delete_attachment($n['attachment']);
            }
            [$file, $name] = $new ?? [$n['attachment'], $n['attachment_name']];
            db()->prepare('UPDATE notes SET title = ?, body = ?, attachment = ?, attachment_name = ? WHERE id = ?')
                ->execute([mb_substr(trim($_POST['title'] ?? ''), 0, 200) ?: 'Untitled', $_POST['body'] ?? '', $file, $name, $n['id']]);
            flash('Saved.');
            redirect("/notes/{$n['id']}");
        }
        if ($method === 'POST' && $action === '/delete') {
            check_csrf();
            db()->prepare('DELETE FROM notes WHERE id = ?')->execute([$n['id']]);
            delete_attachment($n['attachment']);
            log_line('note deleted', ['id' => $n['id']]);
            flash('Deleted.');
            redirect('/');
        }
    }
    if ($method === 'GET' && preg_match('#^/files/([a-f0-9]{32})$#', $path, $m) && is_file(ATTACHMENTS . '/' . $m[1])) {
        $st = db()->prepare('SELECT attachment_name FROM notes WHERE attachment = ?');
        $st->execute([$m[1]]);
        $name = (string) ($st->fetchColumn() ?: 'attachment');
        header('Content-Type: application/octet-stream');
        header('Content-Disposition: attachment; filename="' . addcslashes($name, '"\\') . '"');
        header('Content-Length: ' . filesize(ATTACHMENTS . '/' . $m[1]));
        readfile(ATTACHMENTS . '/' . $m[1]);
        exit;
    }
    http_response_code(404);
    page('Not found', '<p>Nothing here.</p>');
} catch (Throwable $e) {
    log_line('request failed', ['path' => $path, 'err' => $e->getMessage()]);
    http_response_code(500);
    page('Error', '<p>' . h($e->getMessage()) . '</p>');
}
