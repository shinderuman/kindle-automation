package main

import (
	"log"
	"os"

	"github.com/shinderuman/kindle-automation/internal/migration/checkerconfig"
)

func main() {
	log.SetFlags(0)
	if err := checkerconfig.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatalf("migrate-checker-config: %v", err)
	}
}
