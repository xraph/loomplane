package main

import (
	"log"
	"net/http"

	"example.com/acme/store"
)

func main() {
	db := store.Open()
	http.HandleFunc("/orders", db.Orders)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
