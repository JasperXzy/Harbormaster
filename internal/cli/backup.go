package cli

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/JasperXzy/harbormaster/internal/backup"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"golang.org/x/term"
)

func exportBackup(ctx context.Context, s *store.Store, box *security.Box, in io.Reader, out io.Writer, path string) error {
	var password []byte
	var err error
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		_, _ = io.WriteString(out, "Backup password: ")
		password, err = term.ReadPassword(int(f.Fd()))
		_, _ = io.WriteString(out, "\n")
	} else {
		password, err = io.ReadAll(io.LimitReader(in, 1026))
		password = bytes.TrimSuffix(password, []byte("\n"))
		password = bytes.TrimSuffix(password, []byte("\r"))
	}
	defer clear(password)
	if err != nil {
		return err
	}
	return backup.ExportFile(ctx, s, box, password, path)
}
