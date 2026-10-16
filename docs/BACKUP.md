# Backups

`piway backup` writes one `.tar.gz` with everything needed to bring a piway
installation back, on this Pi or a new one. `piway restore` puts it back.

```sh
piway backup                          # piway-backup-20261016-0830.tar.gz here
piway backup -o /mnt/usb/piway.tar.gz
piway backup -o - | ssh nas 'cat > piway.tar.gz'
```

**The archive holds secrets**: app secrets, deploy keys, the GitHub token,
the dashboard password hash and API token hashes. piway writes it readable
only by you (mode 0600). Keep it that way, and keep a copy off the Pi: a
backup on the same SD card does not survive the card.

## What is in it

| Part | From | Notes |
| ---- | ---- | ----- |
| Configuration | `~/.config/piway/` (the directory of `apps.yaml`) | `config.yaml`, `apps.yaml`, `alerts.yaml`, `secrets/`, `keys/`, `password`. |
| State | the data directory (`~/.local/state/piway/`) | Deploy records and their piway.yaml files, API tokens, push subscriptions and the push key. Sign-in sessions are left out: sign in again. |
| Metrics history | the data directory | Only with `-metrics`; it can be tens of megabytes. |
| Volumes | every app volume (`run.volumes`) | Files with their owners and modes. SQLite databases use SQLite's online backup (below). |

Images are not included: after a restore, piway builds each app again from
its repository on its next deploy.

### SQLite databases

Copying a database file while an app writes to it can give a corrupt copy,
and with WAL mode the newest data may still be in the `-wal` file. So piway
copies every `*.db`, `*.sqlite` and `*.sqlite3` file that starts with
SQLite's header through SQLite's online backup (`sqlite3 .backup`), which
gives a consistent copy while the app keeps running. The archive has the
database file alone, without `-wal`, `-shm` or `-journal` files, owned like
the original. `MANIFEST.json` in the archive lists the databases copied this
way.

Volumes are read in a small helper container, `piway/backup-helper:1`
(Alpine with SQLite and GNU tar), which piway builds the first time; that
first run needs the network. With rootless Docker this is also how piway
reads files an app wrote as a non-root user. The helper has no network
while it reads.

## Restoring

Stop piway first, so it does not write while files are replaced:

```sh
systemctl --user stop piway
piway restore piway-backup-20261016-0830.tar.gz
systemctl --user start piway
```

On a new Pi, install piway and rootless Docker first ([INSTALL.md](INSTALL.md)),
then restore. Then deploy each app once (from its page, or with a push):
the volumes are already there, and the new containers mount them.

`piway restore` checks the whole archive before it writes anything. If a
file or a volume it would write exists already, it changes nothing and
lists them; `-force` replaces them. A volume replaced with `-force` is
emptied first, so it ends up exactly as backed up. Stop or remove an app's
container before forcing its volume back, or the app keeps writing to it.

## On a schedule

A systemd user timer runs a backup every night at 03:00:

```ini
# ~/.config/systemd/user/piway-backup.service
[Unit]
Description=piway backup

[Service]
Type=oneshot
WorkingDirectory=%h/backups
ExecStart=%h/.local/bin/piway backup
```

```ini
# ~/.config/systemd/user/piway-backup.timer
[Timer]
OnCalendar=*-*-* 03:00
Persistent=true

[Install]
WantedBy=timers.target
```

```sh
mkdir -p ~/backups
systemctl --user daemon-reload
systemctl --user enable --now piway-backup.timer
```

Old archives are not deleted for you; something like
`find ~/backups -name 'piway-backup-*.tar.gz' -mtime +14 -delete` in the
same service keeps two weeks.
