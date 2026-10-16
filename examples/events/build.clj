(ns build
  (:require [clojure.tools.build.api :as b]))

(def class-dir "target/classes")
(def basis (delay (b/create-basis {:project "deps.edn"})))

;; clojure -T:build uber: compiles events.main ahead of time, so the JVM
;; starts without compiling Clojure (seconds on a Pi).
(defn uber [_]
  (b/delete {:path "target"})
  (b/copy-dir {:src-dirs ["src" "resources"] :target-dir class-dir})
  (b/compile-clj {:basis @basis :ns-compile '[events.main] :class-dir class-dir})
  (b/uber {:class-dir class-dir :uber-file "target/events.jar" :basis @basis :main 'events.main}))
