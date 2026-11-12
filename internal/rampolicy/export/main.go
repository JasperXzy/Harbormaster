// Command export writes the fixed policy documents, never applies them.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JasperXzy/harbormaster/internal/rampolicy"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/rampolicy/export OUTPUT_DIRECTORY")
		os.Exit(2)
	}
	for _, draft := range rampolicy.Catalog() {
		raw, err := json.MarshalIndent(draft.Document, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(os.Args[1], draft.ID+".json"), append(raw, '\n'), 0644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
