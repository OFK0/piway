# Puma has a C extension: compile the gems in one stage, run without the
# compilers in the next.
FROM ruby:3.4-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY Gemfile Gemfile.lock ./
RUN bundle config set --local deployment true && bundle config set --local without development \
 && bundle install --jobs 4

FROM ruby:3.4-slim
WORKDIR /app
COPY --from=build /app/vendor /app/vendor
COPY --from=build /app/.bundle /app/.bundle
COPY Gemfile Gemfile.lock app.rb config.ru puma.rb index.html ./
# An unprivileged user, and a /data it owns: a new volume mounted there
# starts as a copy of it, so the app can write its database.
RUN useradd --uid 10001 --no-create-home app && mkdir /data && chown app /data
USER app
ENV DATA_DIR=/data PORT=4567 RACK_ENV=production
EXPOSE 4567
CMD ["bundle", "exec", "puma", "-C", "puma.rb", "config.ru"]
