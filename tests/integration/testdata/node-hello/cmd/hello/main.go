// Command hello is the service TestNodeDeploysAndServes builds from source
// on a node and serves through a domain. It answers every request with one
// line naming itself and the GREETING it was given.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	greeting := os.Getenv("GREETING")
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "hello from a node: %s\n", greeting)
	})
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
