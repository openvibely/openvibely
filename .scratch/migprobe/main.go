package main

import (
	"log"
	"os"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
)

func main() {
	db, err := database.New(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	db.Close()
}
