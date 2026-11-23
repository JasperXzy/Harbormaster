// This build helper validates a version before passing it to the Go linker.
package main

import (
	"fmt"
	"os"

	"github.com/JasperXzy/harbormaster/internal/agentrelease"
)

func main() {
	if len(os.Args) == 2 {
		if version, ok := agentrelease.NormalizeVersion(os.Args[1]); ok {
			fmt.Println(version)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "agent_release_version_must_be_full_semver_or_dev")
	os.Exit(1)
}
