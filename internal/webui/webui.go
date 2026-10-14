// Package webui provides the built dashboard (web/) to the HTTP server.
//
// The files are embedded only when piway is built with -tags embedui, after
// the dashboard has been built into dist/ (make build does both). Without
// the tag FS returns nil and piway serves the API alone, so `go test ./...`,
// `go vet` and `go run` work on a machine without Node.
package webui
