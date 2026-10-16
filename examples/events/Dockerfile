# Build an uberjar with tools.build (ahead-of-time compiled), run it on a JRE.
FROM clojure:temurin-21-tools-deps AS build
WORKDIR /src
COPY deps.edn build.clj ./
# Fetch the dependencies in their own cached layer.
RUN clojure -P && clojure -T:build help/doc >/dev/null 2>&1 || true
COPY src src
COPY resources resources
RUN clojure -T:build uber

FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/target/events.jar .
RUN useradd --uid 10001 --no-create-home app && mkdir /data && chown app /data
USER app
ENV DATA_DIR=/data PORT=8080 \
    JAVA_TOOL_OPTIONS="-XX:MaxRAMPercentage=60 -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -Xss512k"
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "events.jar"]
