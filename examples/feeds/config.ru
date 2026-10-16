require_relative 'app'

Feeds.migrate
Feeds.start_scheduler
run Feeds::App
