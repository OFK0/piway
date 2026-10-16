(ns events.main
  "events: an example app for piway.

  Takes in events over HTTP, aggregates them in time windows with
  core.async, streams the statistics over a WebSocket every second, and
  snapshots the totals to disk as EDN, so a new version starts where the
  old one stopped.

    POST /api/events            {\"type\": \"click\", \"value\": 1.5}, or an array of them
    POST /api/generate?rate=200&seconds=10   synthetic events, for a demo or a test
    GET  /api/stats             the latest statistics
    GET  /ws                    the statistics every second (WebSocket)
    GET  /api/info, /healthz    the example apps' common contract"
  (:gen-class)
  (:require [clojure.core.async :as a]
            [clojure.data.json :as json]
            [clojure.edn :as edn]
            [clojure.java.io :as io]
            [clojure.string :as str]
            [org.httpkit.server :as http])
  (:import (java.nio.file AtomicMoveNotSupportedException Files StandardCopyOption)
           (java.security MessageDigest)
           (java.time Instant)))

(def data-dir (or (System/getenv "DATA_DIR") "/data"))
(def snapshot-file (io/file data-dir "snapshot.edn"))
(def windows {"10s" 10 "1m" 60 "5m" 300})

(defn log [msg & {:as fields}]
  (locking *out*
    (println (json/write-str (merge {:time (str (Instant/now)) :msg msg} fields)))
    (flush)))

(defn now-s [] (quot (System/currentTimeMillis) 1000))

;; Events come in on a buffered channel; when the aggregator falls behind,
;; the buffer fills and producers get 503 instead of piling up in memory.
(def incoming (a/chan (a/buffer 10000)))

;; The aggregator's state: per-second buckets of the last five minutes, and
;; the totals: :base, handed over by the previous version through the
;; snapshot, plus :own, what this instance counted.
(def instance (str (java.util.UUID/randomUUID)))
(def no-totals {:events 0 :by-type {}})
(def state (atom {:buckets (sorted-map) :base no-totals :own no-totals :started (str (Instant/now))}))

(defn totals [{:keys [base own]}]
  {:events (+ (:events base) (:events own))
   :by-type (merge-with + (:by-type base) (:by-type own))})
(def latest (atom {}))

(defn clean-type
  "An event's type: its text, trimmed to 40 characters; \"event\" if none."
  [t]
  (let [s (str/trim (str (or t "")))]
    (if (str/blank? s) "event" (subs s 0 (min 40 (count s))))))

(defn add-event [st {:keys [type value]} sec]
  (let [type (clean-type type)
        v (when (number? value) (double value))]
    (-> st
        (update-in [:buckets sec type]
                   (fn [{:keys [n sum mn mx] :or {n 0 sum 0.0}}]
                     {:n (inc n)
                      :sum (+ sum (or v 0.0))
                      :mn (if v (if mn (min mn v) v) mn)
                      :mx (if v (if mx (max mx v) v) mx)}))
        (update-in [:own :events] inc)
        (update-in [:own :by-type type] (fnil inc 0)))))

(defn window-stats [buckets from]
  (reduce (fn [acc [_ by-type]]
            (reduce (fn [acc [type {:keys [n sum mn mx]}]]
                      (update acc type (fn [{an :n asum :sum amn :min amx :max :or {an 0 asum 0.0}}]
                                         {:n (+ an n)
                                          :sum (+ asum sum)
                                          :min (if (and amn mn) (min amn mn) (or amn mn))
                                          :max (if (and amx mx) (max amx mx) (or amx mx))})))
                    acc by-type))
          {} (subseq buckets >= from)))

(defn stats [{:keys [buckets started] :as st}]
  (let [now (now-s)]
    {:time (str (Instant/now))
     :since started
     :totals (totals st)
     :windows (into {}
                    (for [[label secs] windows]
                      (let [per-type (window-stats buckets (- now secs))
                            n (reduce + (map :n (vals per-type)))]
                        [label {:events n
                                :per_second (/ (Math/round (* 100.0 (/ n secs))) 100.0)
                                :by_type (into {} (for [[t {:keys [n sum min max]}] per-type]
                                                    [t {:n n :mean (when (pos? n) (/ (Math/round (* 100.0 (/ sum n))) 100.0))
                                                        :min min :max max}]))}])))}))

;; Clients of /ws, each an http-kit channel.
(def clients (atom #{}))

(defn aggregator
  "Takes events, and once a second drops old buckets, recomputes the
  statistics and sends them to every WebSocket."
  []
  (a/go-loop [tick (a/timeout 1000)]
    (let [[ev ch] (a/alts! [incoming tick])]
      (cond
        (= ch tick)
        (let [cutoff (- (now-s) (apply max (vals windows)))
              st (swap! state update :buckets #(into (sorted-map) (subseq % >= cutoff)))
              s (stats st)
              msg (json/write-str s)]
          (reset! latest s)
          (doseq [c @clients] (http/send! c msg))
          (recur (a/timeout 1000)))

        (some? ev)
        (do (swap! state add-event ev (now-s))
            (recur tick))))))

(defn write-snapshot!
  "Writes the totals, the start time and who wrote them as EDN, atomically.
  final? marks the last one an instance writes, at shutdown."
  [final?]
  (let [tmp (io/file data-dir (str "snapshot.edn." instance ".tmp"))]
    (spit tmp (pr-str {:totals (totals @state) :started (:started @state) :instance instance :final final?}))
    (try
      (Files/move (.toPath tmp) (.toPath snapshot-file) (into-array [StandardCopyOption/ATOMIC_MOVE StandardCopyOption/REPLACE_EXISTING]))
      (catch AtomicMoveNotSupportedException _
        (Files/move (.toPath tmp) (.toPath snapshot-file) (into-array [StandardCopyOption/REPLACE_EXISTING]))))))

(defn read-snapshot []
  (when (.exists snapshot-file)
    (edn/read-string (slurp snapshot-file))))

;; Handing the totals over during a deploy. The new version starts while
;; the old one still runs and counts, and the old one writes its final
;; snapshot only when it stops, after piway switched traffic. So a new
;; version that finds a snapshot that is not final waits for the final
;; one (up to two minutes), writes none of its own meanwhile (it would
;; overwrite the old one's), and then takes it as its base.
(def handed-over (atom true))

(defn take-over! [snap]
  (swap! state assoc :base (:totals snap) :started (:started snap))
  (reset! handed-over true)
  (log "took over the totals" :events (get-in snap [:totals :events]) :from (:instance snap) :final (:final snap)))

(defn load-snapshot! []
  (when-let [snap (read-snapshot)]
    (take-over! snap)
    (when-not (:final snap)
      (reset! handed-over false)
      (a/go-loop [tries 60]
        (a/<! (a/timeout 2000))
        (let [snap (read-snapshot)]
          (cond
            (and (:final snap) (not= instance (:instance snap))) (take-over! snap)
            (zero? tries) (do (reset! handed-over true)
                              (log "no final snapshot from the previous version; keeping the last one"))
            :else (recur (dec tries))))))))

(defn json-response [status body]
  {:status status :headers {"Content-Type" "application/json"} :body (json/write-str body)})

(defn accept-events [evs]
  (let [evs (if (sequential? evs) evs [evs])
        accepted (count (take-while #(a/offer! incoming {:type (clean-type (get % "type")) :value (get % "value")})
                                    (take 10000 evs)))]
    (if (= accepted (count evs))
      (json-response 202 {:accepted accepted})
      (json-response 503 {:accepted accepted :error "busy: the event buffer is full"}))))

(defn generate!
  "Puts rate events a second, for seconds, of a few types with values."
  [rate seconds]
  (let [types ["click" "view" "purchase" "error"]]
    (a/go
      (dotimes [_ seconds]
        (dotimes [_ rate]
          (a/>! incoming {:type (rand-nth types) :value (rand 100)}))
        (a/<! (a/timeout 1000)))
      (log "generated" :rate rate :seconds seconds))))

(defn sha256-prefix [s]
  (let [d (.digest (MessageDigest/getInstance "SHA-256") (.getBytes (str s) "UTF-8"))]
    (str/join (map #(format "%02x" (bit-and % 0xff)) (take 4 d)))))

(defn param [req k default]
  (or (some->> (:query-string req) (re-find (re-pattern (str "(?:^|&)" k "=(\\d+)"))) second parse-long) default))

(defn handler [{:keys [request-method uri] :as req}]
  (try
    (case [request-method uri]
      [:get "/healthz"] {:status 200 :body "ok\n"}
      [:get "/api/info"]
      (let [secret (or (System/getenv "EVENTS_TOKEN") "")]
        (json-response 200 {:app "events" :language "Clojure"
                            :runtime (str "Clojure " (clojure-version) ", Java " (System/getProperty "java.version"))
                            :env {:GREETING (or (System/getenv "GREETING") "")}
                            :secret {:name "EVENTS_TOKEN" :set (not (str/blank? secret)) :length (count secret)
                                     :sha256_prefix (sha256-prefix secret)}}))
      [:get "/"] {:status 200 :headers {"Content-Type" "text/html; charset=utf-8"} :body (slurp (io/resource "index.html"))}
      [:get "/api/stats"] (json-response 200 @latest)
      [:post "/api/events"] (accept-events (json/read-str (slurp (:body req))))
      [:post "/api/generate"]
      (let [rate (min 5000 (param req "rate" 200)) seconds (min 600 (param req "seconds" 10))]
        (generate! rate seconds)
        (json-response 202 {:rate rate :seconds seconds}))
      [:get "/ws"]
      (http/as-channel req {:on-open (fn [ch] (swap! clients conj ch) (http/send! ch (json/write-str @latest)))
                            :on-close (fn [ch _] (swap! clients disj ch))})
      (json-response 404 {:error "not found"}))
    (catch Exception e
      (log "request failed" :uri uri :err (.getMessage e))
      (json-response 400 {:error (.getMessage e)}))))

(defn -main [& _]
  (.mkdirs (io/file data-dir))
  (load-snapshot!)
  (aggregator)
  (let [port (parse-long (or (System/getenv "PORT") "8080"))
        stop (http/run-server handler {:port port :thread 4 :legacy-return-value? false})
        snapshots (a/go-loop []
                    (a/<! (a/timeout 30000))
                    (when @handed-over (write-snapshot! false))
                    (recur))]
    ;; SIGTERM: stop taking requests (letting those in flight finish, up to
    ;; 5 s), close the WebSockets, and write a last snapshot for the next
    ;; version to start from.
    (.addShutdownHook (Runtime/getRuntime)
                      (Thread. (fn []
                                 (log "shutting down" :clients (count @clients))
                                 (doseq [c @clients] (http/close c))
                                 (http/server-stop! stop {:timeout 5000})
                                 (a/close! snapshots)
                                 (write-snapshot! true)
                                 (log "final snapshot written" :events (:events (totals @state))))))
    (log "listening" :port port :data data-dir)))
