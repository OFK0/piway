// chat: an example app for piway.
//
// Rooms over WebSocket. Every message goes through Redis pub/sub, so any
// number of instances serve the same rooms, and the last 100 messages of
// each room are kept in Redis as its history. Attachments are files on the
// data volume. Redis comes from run.databases: piway gives the app its own
// ACL user, limited to keys and channels under REDIS_PREFIX.
//
//   WS   /ws?room=lobby&name=ada        join a room; send {"text": "..."}
//   GET  /api/rooms/:room/history       the last messages
//   POST /api/rooms/:room/attachments   the raw file as the body,
//                                       X-Filename: its name; posts a message
//   GET  /files/:id                     an attachment
//   GET  /api/info, /healthz            the example apps' common contract

import { createHash, randomUUID } from 'node:crypto'
import { createReadStream, createWriteStream, mkdirSync, statSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import http from 'node:http'
import path from 'node:path'
import { pipeline } from 'node:stream/promises'
import { createClient } from 'redis'
import { WebSocketServer } from 'ws'

const port = Number(process.env.PORT ?? 3000)
const dataDir = process.env.DATA_DIR ?? '/data'
const prefix = process.env.REDIS_PREFIX ?? 'chat:'
const maxAttachment = 10 << 20
const historyLength = 100
const log = (msg, fields = {}) => console.log(JSON.stringify({ time: new Date().toISOString(), msg, ...fields }))

mkdirSync(dataDir, { recursive: true })

// One connection for commands, one for subscriptions (Redis needs both).
const redis = createClient({ url: process.env.REDIS_URL })
const sub = redis.duplicate()
redis.on('error', (err) => log('redis error', { err: err.message }))
sub.on('error', (err) => log('redis error', { err: err.message }))
await Promise.all([redis.connect(), sub.connect()])

const roomKey = (room) => `${prefix}room:${room}`
const validRoom = (room) => /^[a-z0-9-]{1,32}$/.test(room)

// Local sockets per room; Redis carries messages between instances. The
// pattern is the app's whole prefix: Redis ACLs compare a PSUBSCRIBE
// pattern literally with the channels the user may use (piway grants
// "&<prefix>*"), so a narrower pattern would be refused.
const rooms = new Map()
await sub.pSubscribe(`${prefix}*`, (raw, channel) => {
  if (!channel.startsWith(`${prefix}room:`)) return
  const room = channel.slice(`${prefix}room:`.length)
  for (const ws of rooms.get(room) ?? []) ws.send(raw)
})

async function post(room, message) {
  const raw = JSON.stringify({ room, at: new Date().toISOString(), ...message })
  await redis.multi().rPush(roomKey(room), raw).lTrim(roomKey(room), -historyLength, -1).exec()
  await redis.publish(roomKey(room), raw)
}

function json(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x')
  const parts = url.pathname.split('/').filter(Boolean)
  try {
    if (req.method === 'GET' && url.pathname === '/healthz') {
      await redis.ping()
      return res.end('ok\n')
    }
    if (req.method === 'GET' && url.pathname === '/api/info') {
      const secret = process.env.CHAT_TOKEN ?? ''
      return json(res, 200, {
        app: 'chat',
        language: 'JavaScript',
        runtime: `Node.js ${process.version}`,
        env: { GREETING: process.env.GREETING ?? '', REDIS_PREFIX: prefix },
        secret: { name: 'CHAT_TOKEN', set: secret !== '', length: secret.length, sha256_prefix: createHash('sha256').update(secret).digest('hex').slice(0, 8) },
      })
    }
    if (req.method === 'GET' && url.pathname === '/') {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
      return res.end(await readFile(new URL('index.html', import.meta.url)))
    }
    if (parts[0] === 'api' && parts[1] === 'rooms' && validRoom(parts[2] ?? '')) {
      const room = parts[2]
      if (req.method === 'GET' && parts[3] === 'history') {
        const items = await redis.lRange(roomKey(room), 0, -1)
        return json(res, 200, items.map((s) => JSON.parse(s)))
      }
      if (req.method === 'POST' && parts[3] === 'attachments') {
        const name = path.basename(String(req.headers['x-filename'] ?? 'file')).slice(0, 100)
        const id = `${randomUUID()}${path.extname(name).slice(0, 10)}`
        const file = path.join(dataDir, id)
        let size = 0
        req.on('data', (chunk) => {
          size += chunk.length
          if (size > maxAttachment) req.destroy(new Error('too large'))
        })
        try {
          await pipeline(req, createWriteStream(file))
        } catch {
          return json(res, 413, { error: `attachments are up to ${maxAttachment >> 20} MB` })
        }
        await post(room, { name: String(req.headers['x-sender'] ?? 'someone').slice(0, 32), text: name, attachment: `/files/${id}`, bytes: size })
        return json(res, 201, { url: `/files/${id}`, bytes: size })
      }
    }
    if (req.method === 'GET' && parts[0] === 'files' && parts.length === 2 && /^[0-9a-f-]{36}(\.[\w]{1,9})?$/.test(parts[1])) {
      const file = path.join(dataDir, parts[1])
      const { size } = statSync(file, { throwIfNoEntry: false }) ?? {}
      if (size === undefined) return json(res, 404, { error: 'no such file' })
      res.writeHead(200, { 'Content-Length': size, 'Content-Disposition': 'inline' })
      return createReadStream(file).pipe(res)
    }
    json(res, 404, { error: 'not found' })
  } catch (err) {
    log('request failed', { path: url.pathname, err: err.message })
    if (!res.headersSent) json(res, 500, { error: err.message })
  }
})

const wss = new WebSocketServer({ server, path: '/ws', maxPayload: 16 << 10 })
wss.on('connection', (ws, req) => {
  const url = new URL(req.url, 'http://x')
  const room = url.searchParams.get('room') ?? 'lobby'
  const name = (url.searchParams.get('name') ?? 'someone').slice(0, 32)
  if (!validRoom(room)) return ws.close(1008, 'room names are a-z, 0-9 and -')
  if (!rooms.has(room)) rooms.set(room, new Set())
  rooms.get(room).add(ws)
  post(room, { name, text: 'joined', system: true }).catch(() => {})
  ws.on('message', (data) => {
    let text
    try {
      text = String(JSON.parse(data).text ?? '').slice(0, 2000)
    } catch {
      return
    }
    if (text) post(room, { name, text }).catch((err) => log('post failed', { err: err.message }))
  })
  ws.on('close', () => {
    rooms.get(room)?.delete(ws)
    post(room, { name, text: 'left', system: true }).catch(() => {})
  })
})

server.listen(port, () => log('listening', { port, dataDir, prefix }))

// On SIGTERM: stop taking connections, tell clients to reconnect (to the
// new version, during a deploy), and close Redis.
process.on('SIGTERM', async () => {
  log('shutting down', { clients: wss.clients.size })
  for (const ws of wss.clients) ws.close(1001, 'restarting')
  wss.close()
  server.close()
  await Promise.allSettled([sub.quit(), redis.quit()])
  process.exit(0)
})
