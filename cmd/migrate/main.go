// Command migrate applies the embedded SQL migration manifest to the
// database named by DATABASE_URL (or the positional DSN argument).
package main

import (
	"fmt"
	"os"

	"checkwallet/internal/platform/database"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if len(os.Args) > 1 {
		dsn = os.Args[1]
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "usage: migrate <dsn>  (or set DATABASE_URL)")
		os.Exit(2)
	}
	if err := database.Migrate(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied")
}
