# feeds: an example app for piway.
#
# Follows RSS and Atom feeds: fetches them on a schedule, keeps the items in
# SQLite on the data volume, and streams new items to the page over
# Server-Sent Events. Feeds come and go as OPML.
#
#   GET  /api/feeds, POST /api/feeds {"url"}, DELETE /api/feeds/:id
#   GET  /api/items?limit=50           newest first
#   POST /api/refresh                  fetch every feed now
#   GET  /events                       new items as they arrive (SSE)
#   GET  /opml, POST /opml             export, import (the OPML document as the body)
#   GET  /api/info, /healthz           the example apps' common contract

require 'digest'
require 'json'
require 'net/http'
require 'rexml/document'
require 'rss'
require 'sinatra/base'
require 'sqlite3'

module Feeds
  DB_PATH = File.join(ENV.fetch('DATA_DIR', '/data'), 'feeds.db')
  FETCH_EVERY = Integer(ENV.fetch('FETCH_EVERY', '600'))

  LOG = Mutex.new
  def self.log(msg, **fields)
    LOG.synchronize { $stdout.puts(JSON.generate({ time: Time.now.utc.iso8601, msg: msg }.merge(fields))) }
    $stdout.flush
  end

  # db is one connection per thread. WAL lets readers and a writer work at
  # once, which is what happens while two versions share the file during a
  # deploy; the busy timeout makes a write wait for the other's lock.
  def self.db
    Thread.current[:feeds_db] ||= SQLite3::Database.new(DB_PATH).tap do |db|
      db.results_as_hash = true
      db.busy_timeout = 5000
      db.execute('PRAGMA journal_mode = WAL')
    end
  end

  def self.migrate
    db.execute_batch(<<~SQL)
      CREATE TABLE IF NOT EXISTS feeds (
        id INTEGER PRIMARY KEY, url TEXT NOT NULL UNIQUE, title TEXT,
        fetched_at TEXT, error TEXT
      );
      CREATE TABLE IF NOT EXISTS items (
        id INTEGER PRIMARY KEY, feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
        guid TEXT NOT NULL, title TEXT, link TEXT, published TEXT, seen_at TEXT NOT NULL,
        UNIQUE (feed_id, guid)
      );
      CREATE INDEX IF NOT EXISTS items_seen ON items (seen_at);
    SQL
  end

  # Subscribers to /events: one queue each.
  SUBSCRIBERS = []
  SUBS = Mutex.new
  def self.broadcast(event)
    data = JSON.generate(event)
    SUBS.synchronize { SUBSCRIBERS.each { |q| q << data } }
  end

  def self.get(url, limit = 5)
    uri = URI(url)
    raise ArgumentError, 'only http and https feeds' unless %w[http https].include?(uri.scheme)

    res = Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == 'https', open_timeout: 10, read_timeout: 20) do |http|
      http.get(uri.request_uri, 'User-Agent' => 'piway-feeds-example/1', 'Accept' => 'application/rss+xml, application/atom+xml, */*')
    end
    if res.is_a?(Net::HTTPRedirection) && limit.positive?
      return get(URI.join(url, res['location']).to_s, limit - 1)
    end
    raise "HTTP #{res.code}" unless res.is_a?(Net::HTTPSuccess)

    res.body
  end

  # fetch reads one feed and stores the items it has not seen. Two
  # versions fetching at once (during a deploy) store each item once: the
  # UNIQUE constraint and INSERT OR IGNORE settle it.
  def self.fetch(feed)
    parsed = RSS::Parser.parse(get(feed['url']), false)
    raise 'not an RSS or Atom feed' unless parsed

    title, entries =
      if parsed.is_a?(RSS::Atom::Feed)
        [parsed.title&.content, parsed.entries.map { |e| [e.id&.content || e.link&.href, e.title&.content, e.link&.href, e.updated&.content] }]
      else
        [parsed.channel.title, parsed.items.map { |i| [i.guid&.content || i.link, i.title, i.link, i.pubDate || i.date] }]
      end
    added = 0
    now = Time.now.utc.iso8601
    entries.first(200).each do |guid, item_title, link, published|
      guid = Digest::SHA256.hexdigest([item_title, link].join("\n")) if guid.nil? || guid.empty?
      db.execute('INSERT OR IGNORE INTO items (feed_id, guid, title, link, published, seen_at) VALUES (?, ?, ?, ?, ?, ?)',
                 [feed['id'], guid, item_title.to_s[0, 300], link.to_s[0, 1000], published&.to_s, now])
      next if db.changes.zero?

      added += 1
      broadcast(type: 'item', feed: title || feed['url'], title: item_title, link: link)
    end
    db.execute('UPDATE feeds SET title = ?, fetched_at = ?, error = NULL WHERE id = ?', [title.to_s[0, 300], now, feed['id']])
    log('fetched', feed: feed['url'], new_items: added)
  rescue StandardError => e
    db.execute('UPDATE feeds SET fetched_at = ?, error = ? WHERE id = ?', [Time.now.utc.iso8601, e.message[0, 300], feed['id']])
    log('fetch failed', feed: feed['url'], err: e.message)
  end

  def self.fetch_all
    db.execute('SELECT * FROM feeds').each { |f| fetch(f) }
    broadcast(type: 'fetched')
  end

  def self.start_scheduler
    Thread.new do
      loop do
        fetch_all
        sleep FETCH_EVERY
      rescue StandardError => e
        log('scheduler', err: e.message)
        sleep 60
      end
    end
  end

  class App < Sinatra::Base
    set :server, :puma
    set :show_exceptions, false
    set :host_authorization, { permitted_hosts: [] } # behind piway's proxy, any Host

    helpers do
      def json(status_code, body)
        content_type :json
        halt status_code, JSON.generate(body)
      end

      # raw_body is the request body even when Rack already read it to
      # parse a form (a client that sends no Content-Type gets a form one).
      def raw_body
        request.body.rewind if request.body.respond_to?(:rewind)
        request.body.read
      end

      def body_json
        JSON.parse(raw_body)
      rescue JSON::ParserError
        json 400, error: 'send JSON'
      end
    end

    get('/healthz') do
      Feeds.db.execute('SELECT 1')
      "ok\n"
    end

    get '/api/info' do
      secret = ENV.fetch('FEEDS_TOKEN', '')
      json 200, app: 'feeds', language: 'Ruby', runtime: "Ruby #{RUBY_VERSION}, Sinatra #{Sinatra::VERSION}",
                env: { GREETING: ENV.fetch('GREETING', '') },
                secret: { name: 'FEEDS_TOKEN', set: !secret.empty?, length: secret.length,
                          sha256_prefix: Digest::SHA256.hexdigest(secret)[0, 8] }
    end

    get('/') { send_file File.join(__dir__, 'index.html') }

    get '/api/feeds' do
      json 200, Feeds.db.execute('SELECT f.*, (SELECT COUNT(*) FROM items i WHERE i.feed_id = f.id) AS items FROM feeds f ORDER BY id')
    end

    post '/api/feeds' do
      url = body_json['url'].to_s.strip
      json 400, error: 'send {"url": "https://…"}' unless url.match?(%r{\Ahttps?://\S+\z})
      Feeds.db.execute('INSERT OR IGNORE INTO feeds (url) VALUES (?)', [url])
      feed = Feeds.db.execute('SELECT * FROM feeds WHERE url = ?', [url]).first
      Thread.new { Feeds.fetch(feed) } # answer now; items arrive over /events
      json 201, feed
    end

    delete '/api/feeds/:id' do
      Feeds.db.execute('PRAGMA foreign_keys = ON')
      Feeds.db.execute('DELETE FROM feeds WHERE id = ?', [params[:id].to_i])
      json 200, removed: Feeds.db.changes
    end

    get '/api/items' do
      limit = params.fetch('limit', 50).to_i.clamp(1, 500)
      json 200, Feeds.db.execute(<<~SQL, [limit])
        SELECT i.title, i.link, i.published, i.seen_at, COALESCE(f.title, f.url) AS feed
        FROM items i JOIN feeds f ON f.id = i.feed_id ORDER BY i.seen_at DESC, i.id DESC LIMIT ?
      SQL
    end

    post '/api/refresh' do
      Thread.new { Feeds.fetch_all }
      json 202, refreshing: Feeds.db.get_first_value('SELECT COUNT(*) FROM feeds')
    end

    # Server-Sent Events: a comment every 15 s keeps proxies from closing
    # an idle stream.
    get '/events' do
      content_type 'text/event-stream'
      headers 'Cache-Control' => 'no-cache', 'X-Accel-Buffering' => 'no'
      q = Queue.new
      Feeds::SUBS.synchronize { Feeds::SUBSCRIBERS << q }
      stream do |out|
        out << ": connected\n\n"
        loop do
          data = q.pop(timeout: 15)
          out << (data ? "data: #{data}\n\n" : ": keep-alive\n\n")
        end
      rescue IOError, Errno::EPIPE, Puma::ConnectionError
        nil
      ensure
        Feeds::SUBS.synchronize { Feeds::SUBSCRIBERS.delete(q) }
      end
    end

    get '/opml' do
      content_type 'text/x-opml'
      attachment 'feeds.opml'
      doc = REXML::Document.new('<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>feeds</title></head><body/></opml>')
      Feeds.db.execute('SELECT url, title FROM feeds ORDER BY id').each do |f|
        doc.root.elements['body'].add_element('outline', 'type' => 'rss', 'text' => f['title'] || f['url'], 'xmlUrl' => f['url'])
      end
      out = +''
      doc.write(out)
      out
    end

    post '/opml' do
      doc = REXML::Document.new(raw_body)
      urls = REXML::XPath.match(doc, '//outline[@xmlUrl]').map { |o| o.attributes['xmlUrl'] }.grep(%r{\Ahttps?://})
      added = urls.count do |u|
        Feeds.db.execute('INSERT OR IGNORE INTO feeds (url) VALUES (?)', [u])
        Feeds.db.changes.positive?
      end
      Thread.new { Feeds.fetch_all } if added.positive?
      json 200, found: urls.size, added: added
    rescue REXML::ParseException
      json 400, error: 'not an OPML document'
    end

    error do
      Feeds.log('request failed', path: request.path, err: env['sinatra.error'].message)
      json 500, error: env['sinatra.error'].message
    end
  end
end
