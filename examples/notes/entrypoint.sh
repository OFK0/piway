#!/bin/sh
# Runs the cleanup every CLEANUP_EVERY seconds next to Apache, then hands
# PID 1 to Apache. The loop dies with the container.
set -e
(
  while sleep "${CLEANUP_EVERY:-3600}"; do
    php /var/www/src/cleanup.php || true
  done
) &
exec apache2-foreground
