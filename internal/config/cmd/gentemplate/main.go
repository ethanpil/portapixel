// Command gentemplate writes os/portapixel.toml from config.Template.
//
// Run it from the repository root after a change to config.Render or
// config.Default:
//
//	go run ./internal/config/cmd/gentemplate
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethanpil/portapixel/internal/config"
)

func main() {
	path := filepath.Join("os", config.FileName)
	if err := os.WriteFile(path, config.Template(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gentemplate: %v (run it from the repository root)\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", path)
}
