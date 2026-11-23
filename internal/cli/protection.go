package cli

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/cloud/aliyunmeta"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

// An unavailable or unmatched metadata identity is UNKNOWN, never an implicit
// declaration that this host is outside the managed fleet. SWAS requires the
// approved explicit binding because its metadata ID is an underlying ECS ID.
func resolveServerIdentity(ctx context.Context, c config.Config, s *store.Store) config.Config {
	if c.Dev || c.ServerNotManaged || c.ServerInstanceID != "" {
		return c
	}
	identity, err := aliyunmeta.Read(ctx, nil)
	if err != nil {
		return c
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM instance WHERE cloud_id=? AND region=? AND kind='ecs'", identity.InstanceID, identity.RegionID)
	if err != nil {
		return c
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return c
		}
		ids = append(ids, id)
	}
	if rows.Err() == nil && len(ids) == 1 {
		c.ServerInstanceID = ids[0]
	}
	return c
}
func protectionPolicy(c config.Config) model.ProtectionPolicy {
	return model.ProtectionPolicy{PowerEnabled: !c.Dev && c.InstancePowerEnabled, SelfKnown: c.ServerNotManaged || c.ServerInstanceID != "", SelfInstanceID: c.ServerInstanceID, Timezone: c.Timezone}
}
