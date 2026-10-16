# notes (PHP)

Notes with attachments: PHP 8.4 under Apache, the notes in MariaDB, the
sessions in Redis, the attachment files on the data volume, and a cleanup
script that runs every hour inside the container.

| Path | What it does |
| ---- | ------------ |
| `GET /` | The notes, newest first. |
| `GET /notes/new`, `POST /notes` | Write a note, with an attachment (up to 5 MB). |
| `GET /notes/{id}`, `GET /notes/{id}/edit`, `POST /notes/{id}` | Read and change one; a new attachment replaces the old file. |
| `POST /notes/{id}/delete` | Delete it, and its attachment file. |
| `GET /files/{id}` | Download an attachment. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract); `/healthz` also queries MariaDB. |

What it shows about piway:

- `run.databases: [mariadb, redis]`: two engines for one app. `DATABASE_URL`
  is MariaDB's; sessions use phpredis's session handler with the app's ACL
  user, under `REDIS_PREFIX` (`notes:sess:…`), so they survive a deploy.
- A schema made on first use with `CREATE TABLE IF NOT EXISTS`: additive,
  so it is safe while the old version still runs during a deploy.
- A scheduled job without a scheduler: the entrypoint runs
  `src/cleanup.php` every `CLEANUP_EVERY` seconds (3600) next to Apache. It
  deletes attachment files no note refers to, older than an hour.
- A graceful stop: the image stops Apache with SIGWINCH, so requests in
  flight finish.
- Forms with a CSRF token kept in the session.

## Deploy

```sh
piway repo add OFK0/piway -path examples/notes
```

Set its secret `NOTES_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First deploy | 5 min 53 s: pulling `php:8.4-apache`, compiling `pdo_mysql` and `phpredis`, then pulling and starting MariaDB and Redis |
| Build with a warm cache | 20 s |
| Image | 732 MB (Debian, Apache, and the compilers `pecl` needed) |
| Memory, idle | 13 MiB of 192 MiB (and, shared, MariaDB 86 MiB and Redis 5 MiB) |
| Graceful stop | 1.6 s, exit code 0 |

A smaller image is possible with a multi-stage build that compiles the
extensions in one stage and copies them into a clean one; this example keeps
the Dockerfile short.
