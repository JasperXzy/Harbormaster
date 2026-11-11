// prepare stages frontend assets within the embed package without writing web/.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed fallback.html
var fallback []byte

func main() {
	source := flag.String("source", "web/dist", "frontend build directory")
	target := flag.String("target", "internal/ui/assets", "generated embed directory")
	flag.Parse()
	if err := prepare(*source, *target); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(source, target string) error {
	src, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	// Never stage onto the source, its parents, or its descendants.
	for _, pair := range [][2]string{{src, dst}, {dst, src}} {
		r, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))) {
			return fmt.Errorf("source and target must be separate trees")
		}
	}
	_, err = os.Stat(src)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return err
	}
	if !missing {
		info, err := os.Stat(filepath.Join(src, "index.html"))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("frontend build must contain index.html")
		}
	}
	// Stage first, then replace only this generated directory.
	if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if missing {
		err = os.WriteFile(filepath.Join(tmp, "index.html"), fallback, 0644)
	} else {
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("frontend symlinks are not supported")
			}
			rel, e := filepath.Rel(src, p)
			if e != nil {
				return e
			}
			to := filepath.Join(tmp, rel)
			if d.IsDir() {
				return os.MkdirAll(to, 0755)
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("unsupported frontend file")
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			return os.WriteFile(to, b, 0644)
		})
	}
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dst); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	if missing {
		fmt.Println("frontend: embedded fallback")
	} else {
		fmt.Println("frontend: staged dist for go:embed")
	}
	return nil
}
