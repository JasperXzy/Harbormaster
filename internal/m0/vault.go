// Package m0 contains local technical-verification tools, not panel APIs.
package m0

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/security"
)

const vaultAAD = "harbormaster/m0/credentials/v1"
const vaultMagic = "HM-M0-1\n"

type profile struct {
	Name            string   `json:"name"`
	Site            string   `json:"site"`
	Regions         []string `json:"regions"`
	AccessKeyID     string   `json:"access_key_id"`
	AccessKeySecret string   `json:"access_key_secret"`
	SecurityToken   string   `json:"security_token,omitempty"`
}
type vault struct {
	Version  int       `json:"version"`
	Accounts []profile `json:"accounts"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var regionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)

func (p profile) validate() error {
	if !namePattern.MatchString(p.Name) || (p.Site != "china" && p.Site != "international") {
		return errors.New("invalid_account_metadata")
	}
	if len(p.Regions) == 0 || len(p.Regions) > 20 {
		return errors.New("invalid_regions")
	}
	seen := map[string]bool{}
	for _, r := range p.Regions {
		if len(r) > 64 || !regionPattern.MatchString(r) || seen[r] {
			return errors.New("invalid_regions")
		}
		seen[r] = true
	}
	if len(p.AccessKeyID) < 8 || len(p.AccessKeyID) > 256 || len(p.AccessKeySecret) < 16 || len(p.AccessKeySecret) > 512 || len(p.SecurityToken) > 8192 {
		return errors.New("invalid_credential_length")
	}
	for _, s := range []string{p.AccessKeyID, p.AccessKeySecret, p.SecurityToken} {
		if strings.ContainsAny(s, "\r\n\x00") {
			return errors.New("invalid_credential_format")
		}
	}
	return nil
}

func defaultPaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", errors.New("home_directory_unavailable")
	}
	return filepath.Join(home, ".local", "share", "harbormaster-m0", "credentials.enc"), filepath.Join(home, ".config", "harbormaster", "keys", "m0.key"), nil
}

// Resolve existing ancestors before rejecting repositories and data-directory
// key placement, so a symlink cannot bypass either rule.
func resolve(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(a)
	if err == nil {
		return r, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(a)
	if parent == a {
		return "", err
	}
	r, err = resolve(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(r, filepath.Base(a)), nil
}
func externalPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path_required")
	}
	if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("secret_file_symlink_rejected")
	}
	a, err := resolve(p)
	if err != nil {
		return "", errors.New("path_resolution_failed")
	}
	for d := filepath.Dir(a); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return "", errors.New("path_must_be_outside_repository")
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return a, nil
}
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("private_directory_create_failed")
	}
	i, err := os.Stat(dir)
	if err != nil || !i.IsDir() {
		return errors.New("invalid_private_directory")
	}
	if runtime.GOOS != "windows" && i.Mode().Perm()&0077 != 0 {
		return errors.New("directory_requires_mode_0700")
	}
	return nil
}
func readPrivate(p string, max int64) ([]byte, error) {
	i, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Size() > max {
		return nil, errors.New("invalid_private_file")
	}
	if runtime.GOOS != "windows" && i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("file_requires_mode_0600")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("private_file_read_failed")
	}
	return b, nil
}

func loadBox(vaultPath, keyFile string, create bool) (*security.Box, error) {
	return loadBoxFromSources(vaultPath, keyFile, create, os.Getenv("HM_MASTER_KEY"), os.Getenv("HM_MASTER_KEY_FILE"))
}

func loadBoxFromSources(vaultPath, keyFile string, create bool, envKey, envFile string) (*security.Box, error) {
	if envKey != "" && envFile != "" {
		return nil, errors.New("ambiguous_master_key_sources")
	}
	if envKey != "" {
		key, err := base64.StdEncoding.DecodeString(envKey)
		if err != nil {
			return nil, errors.New("invalid_master_key")
		}
		defer clear(key)
		return security.NewBox(key)
	}
	if envFile != "" {
		keyFile = envFile
		create = false
	}
	k, err := externalPath(keyFile)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(filepath.Dir(vaultPath), k)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, errors.New("master_key_must_be_outside_vault_directory")
	}
	b, err := readPrivate(k, 1024)
	if os.IsNotExist(err) && create {
		if err = privateDir(filepath.Dir(k)); err != nil {
			return nil, err
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, errors.New("random_source_failed")
		}
		defer clear(key)
		b = []byte(base64.StdEncoding.EncodeToString(key))
		f, e := os.OpenFile(k, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, errors.New("master_key_create_failed")
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			_ = os.Remove(k)
			return nil, errors.New("master_key_write_failed")
		}
		err = nil
	}
	if err != nil {
		return nil, errors.New("master_key_unavailable_or_not_private")
	}
	defer clear(b)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, errors.New("invalid_master_key")
	}
	defer clear(key)
	return security.NewBox(key)
}

func readVault(path string, box *security.Box) (vault, error) {
	b, err := readPrivate(path, 1<<20)
	if err != nil {
		return vault{}, err
	}
	if !bytes.HasPrefix(b, []byte(vaultMagic)) {
		return vault{}, errors.New("invalid_vault")
	}
	plain, err := box.Decrypt(b[len(vaultMagic):], vaultAAD)
	if err != nil {
		return vault{}, errors.New("vault_decryption_failed")
	}
	defer clear(plain)
	var v vault
	if json.Unmarshal(plain, &v) != nil || v.Version != 1 {
		return v, errors.New("invalid_vault")
	}
	seen := map[string]bool{}
	for _, p := range v.Accounts {
		if p.validate() != nil || seen[p.Name] {
			return v, errors.New("invalid_vault")
		}
		seen[p.Name] = true
	}
	return v, nil
}

func saveProfile(path, key string, p profile, replace bool) error {
	if err := p.validate(); err != nil {
		return err
	}
	path, err := externalPath(path)
	if err != nil {
		return err
	}
	if err = privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	// A second importer must not overwrite a concurrent import's account list.
	if err = os.Mkdir(path+".lock", 0700); err != nil {
		return errors.New("vault_locked")
	}
	defer os.Remove(path + ".lock")
	_, statErr := os.Lstat(path)
	isNew := os.IsNotExist(statErr)
	box, err := loadBox(path, key, isNew)
	if err != nil {
		return err
	}
	v := vault{Version: 1, Accounts: []profile{}}
	if !isNew {
		v, err = readVault(path, box)
		if err != nil {
			return errors.New("vault_unavailable_or_invalid")
		}
	}
	found := false
	for i, a := range v.Accounts {
		if a.Name == p.Name {
			if !replace {
				return errors.New("account_exists_use_replace")
			}
			v.Accounts[i] = p
			found = true
		}
	}
	if !found {
		v.Accounts = append(v.Accounts, p)
	}
	return encryptVault(path, box, v)
}

// setRegions changes metadata under the same lock as import, preserving the
// latest stored credentials and other accounts even if an import runs nearby.
func setRegions(path, key, name string, regions []string) error {
	path, err := externalPath(path)
	if err != nil {
		return err
	}
	if err = os.Mkdir(path+".lock", 0700); err != nil {
		return errors.New("vault_locked_or_unavailable")
	}
	defer os.Remove(path + ".lock")
	box, err := loadBox(path, key, false)
	if err != nil {
		return err
	}
	v, err := readVault(path, box)
	if err != nil {
		return errors.New("vault_unavailable_or_invalid")
	}
	for i := range v.Accounts {
		if v.Accounts[i].Name == name {
			v.Accounts[i].Regions = regions
			if err = v.Accounts[i].validate(); err != nil {
				return err
			}
			return encryptVault(path, box, v)
		}
	}
	return errors.New("account_not_imported")
}

func encryptVault(path string, box *security.Box, v vault) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return errors.New("vault_encode_failed")
	}
	defer clear(plain)
	ciphertext, err := box.Encrypt(plain, vaultAAD)
	if err != nil {
		return errors.New("vault_encryption_failed")
	}
	return writePrivate(path, append([]byte(vaultMagic), ciphertext...))
}

func writePrivate(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hm-write-")
	if err != nil {
		return errors.New("private_file_create_failed")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("private_file_write_failed")
	}
	if err = os.Rename(tmp, path); err != nil {
		return errors.New("private_file_replace_failed")
	}
	return nil
}

func getProfile(path, key, name string) (profile, error) {
	path, err := externalPath(path)
	if err != nil {
		return profile{}, err
	}
	box, err := loadBox(path, key, false)
	if err != nil {
		return profile{}, err
	}
	v, err := readVault(path, box)
	if err != nil {
		return profile{}, errors.New("vault_unavailable_or_invalid")
	}
	for _, p := range v.Accounts {
		if p.Name == name {
			return p, nil
		}
	}
	return profile{}, errors.New("account_not_imported")
}
