# chat (Node.js)

Chat rooms over WebSocket. Every message goes through Redis pub/sub, so
any number of instances serve the same rooms, and Redis keeps the last
100 messages of each room. Attachments are files on the data volume.

| Endpoint | What it does |
| -------- | ------------ |
| `WS /ws?room=lobby&name=ada` | Join a room; send `{"text": "…"}`; receive every message of the room. |
| `GET /api/rooms/{room}/history` | The room's last messages. |
| `POST /api/rooms/{room}/attachments` | The file as the body (up to 10 MB), `X-Filename` its name: stored, and posted to the room as a link. |
| `GET /files/{id}` | An attachment. |
| `GET /` | A page to join a room and chat. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract); `/healthz` also pings Redis. |

What it shows about piway:

- `run.databases: [redis]`: the first deploy starts piway's shared Redis,
  creates the app's ACL user and sets `REDIS_URL` and `REDIS_PREFIX`. Keys
  and channels are all under the prefix (`chat:`), the only ones the user may
  touch ([DATABASES.md](../../docs/DATABASES.md)).
- Redis pub/sub with an ACL user: `PSUBSCRIBE` patterns are compared
  literally with the channels the user may use, so the app subscribes to
  `chat:*` (exactly what piway grants) and filters, rather than to
  `chat:room:*`.
- Deploys with connected clients: on SIGTERM the server closes every socket
  with code 1001, and the page reconnects, to the new version; the history
  is in Redis, so nothing is lost.

## Deploy

```sh
piway repo add OFK0/piway -path examples/chat
```

Set its secret `CHAT_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First deploy | 43 s, including pulling `node:24-alpine` and `redis:7.4-alpine` and starting Redis |
| Build with a warm cache | 18 s |
| Image | 247 MB |
| Memory, idle | 36 MiB of 128 MiB (and Redis, shared, 4 MiB) |
