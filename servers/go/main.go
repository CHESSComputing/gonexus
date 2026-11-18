// Command nexus-s3-server is a small HTTP server that exposes NeXus
// (HDF5) files stored in an S3 bucket for browsing over plain REST/JSON,
// using the gonexus library to read the file's tree structure and data.
//
// Since HDF5 files must be read from a real filesystem path, each object
// is transparently downloaded to a local disk cache (keyed by its S3
// ETag) on first request and re-used afterwards.
//
// Endpoints:
//
//	GET /healthz
//	GET /files                                  list available NeXus files
//	GET /tree?key=...                           full tree, text/plain
//	GET /entries?key=...                        top-level NXentry groups
//	GET /group?key=...&path=...                 a group's direct children
//	GET /field?key=...&path=...&offset=&limit=  a field's metadata + data
//	POST /view                                  open the UI on a file for a valid token (see AUTH.md)
//	POST /viewform                              the same, payload and token from a web form
//	GET /ui/                                    interactive web UI (embedded)
//
// See ../README.md for configuration (all via environment variables) and
// full endpoint documentation.
package main

// main.go holds only main() and the command-line flags. The server itself is
// in server.go and the HTTP handlers in handlers.go.

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	var version bool
	flag.BoolVar(&version, "version", false, "show version and exit")
	cfile := os.Getenv("CONFIG_FILE")
	var config string
	flag.StringVar(&config, "config", cfile, "path to config file (JSON or YAML), default $CONFIG_FILE")
	flag.Parse()
	if version {
		fmt.Println("server version:", Info())
		return
	}
	cfg, err := loadConfig(config)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	Server(cfg)
}
