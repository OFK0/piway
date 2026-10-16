# Build with Maven, run on a JRE. The dependency layer is cached, so later
# builds only compile the app.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml .
RUN mvn -B -q dependency:go-offline
COPY src src
RUN mvn -B -q -o package -DskipTests

FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/target/reports.jar .
RUN useradd --uid 10001 --no-create-home app && mkdir /data && chown app /data
USER app
# The JVM sizes its heap from the container's memory limit (run.memory):
# 60% of it, leaving room for metaspace, threads and buffers. SerialGC is
# the leanest collector for a small heap on few cores.
ENV DATA_DIR=/data PORT=8080 \
    JAVA_TOOL_OPTIONS="-XX:MaxRAMPercentage=60 -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -Xss512k"
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "reports.jar"]
