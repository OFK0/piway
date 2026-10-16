# One process, threads for requests and SSE streams.
port ENV.fetch('PORT', '4567')
threads 0, 16
# On SIGTERM Puma stops taking connections and waits for requests in
# flight; SSE streams never end on their own, so they are cut after 5 s
# and the page's EventSource reconnects, to the new version during a deploy.
force_shutdown_after 5
# Exit 0 after a graceful SIGTERM, instead of re-raising the signal.
raise_exception_on_sigterm false
