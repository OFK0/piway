<?php
// The scheduled cleanup: deletes attachment files no note refers to any
// more (left by a crash between storing a file and saving its note, or by
// hand). Run every hour by the entrypoint; prints one JSON line.

declare(strict_types=1);

require __DIR__ . '/lib.php';

$used = array_flip(db()->query('SELECT attachment FROM notes WHERE attachment IS NOT NULL')->fetchAll(PDO::FETCH_COLUMN));
$removed = 0;
$kept = 0;
foreach (glob(ATTACHMENTS . '/*') ?: [] as $file) {
    $id = basename($file);
    // Files younger than an hour may belong to an upload still in progress.
    if (isset($used[$id]) || filemtime($file) > time() - 3600) {
        $kept++;
        continue;
    }
    unlink($file);
    $removed++;
}
log_line('cleanup', ['removed' => $removed, 'kept' => $kept]);
