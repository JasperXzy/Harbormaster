package m0

import (
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// ImportAccounts decrypts the existing verification vault in memory and hands
// each record directly to the production encrypted store. It never serializes
// plaintext credentials to stdout, a temporary file, or a job payload.
func ImportAccounts(path, key string, save func(model.AccountCreate) error) error {
	if path == "" || key == "" {
		p, k, err := defaultPaths()
		if err != nil {
			return err
		}
		if path == "" {
			path = p
		}
		if key == "" {
			key = k
		}
	}
	resolved, err := externalPath(path)
	if err != nil {
		return err
	}
	// HM_MASTER_KEY(_FILE) belongs to the destination database in this command.
	box, err := loadBoxFromSources(resolved, key, false, "", "")
	if err != nil {
		return err
	}
	v, err := readVault(resolved, box)
	if err != nil {
		return errors.New("source_vault_unavailable")
	}
	for _, p := range v.Accounts {
		if p.SecurityToken != "" {
			return errors.New("temporary_credentials_require_explicit_support")
		}
	}
	for i := range v.Accounts {
		p := &v.Accounts[i]
		in := model.AccountCreate{Name: p.Name, Site: p.Site, RegionIDs: p.Regions, Credential: model.Credential{AccessKeyID: p.AccessKeyID, AccessKeySecret: p.AccessKeySecret}}
		if err := save(in); err != nil {
			return err
		}
		p.AccessKeyID = ""
		p.AccessKeySecret = ""
	}
	return nil
}
