# Builds natively on the Pi (linux/arm64); piway passes --platform.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /loadlab . \
 && mkdir -p /data

# No shell, no package manager: the binary, and an empty /data owned by
# the user it runs as. A new volume mounted there starts as a copy of it,
# so the app can write to it without running as root.
FROM scratch
COPY --from=build /loadlab /loadlab
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
ENV DATA_DIR=/data PORT=8080
EXPOSE 8080
ENTRYPOINT ["/loadlab"]
