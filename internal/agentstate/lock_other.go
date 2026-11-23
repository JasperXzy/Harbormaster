//go:build !linux && !darwin

package agentstate

import "os"

func lockDirectory(string) (*os.File, error) { return nil, ErrState }
