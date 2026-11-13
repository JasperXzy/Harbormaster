package api

import (
	"context"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) overlaySWASTraffic(ctx context.Context, items []map[string]any) error {
	targets := []model.SWASTrafficTarget{}
	for _, item := range items {
		item["swas_traffic_package"] = nil
		if item["kind"] == "swas" {
			plan, _ := item["spec"].(string)
			targets = append(targets, model.SWASTrafficTarget{InstanceID: item["id"].(string), PlanID: plan})
		}
	}
	values, err := p.s.SWASTrafficPackages(ctx, targets, p.now().UTC())
	if err != nil {
		return err
	}
	for _, item := range items {
		if item["kind"] == "swas" {
			v, ok := values[item["id"].(string)]
			if !ok {
				v = model.EmptySWASTrafficPackage()
			}
			item["swas_traffic_package"] = v
		}
	}
	return nil
}
