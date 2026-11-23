package aliyun

import (
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func idleBase(a model.Account, region, kind, id, name string, row map[string]any, at time.Time) model.IdleCandidate {
	if name == "" {
		name = id
	}
	return model.IdleCandidate{ID: resourceID(a, kind, region, id), AccountID: a.ID, RegionID: region, Kind: kind, CloudID: id, Name: name, ChargeType: charge(row), CloudStatus: text(row, "Status"), InstanceIDs: []string{}, ObservedAt: at}
}

func idleInstance(a model.Account, region, kind string, row map[string]any, at time.Time) (*model.IdleCandidate, bool) {
	switch text(row, "Status") {
	case "Running", "Starting", "Stopping", "Pending":
		return nil, true
	case "Stopped":
	default:
		return nil, false
	}
	var reason string
	rawCharge := text(row, "InstanceChargeType")
	if kind == "swas" {
		rawCharge = text(row, "ChargeType")
	}
	switch rawCharge {
	case "PrePaid":
		if strategy := text(row, "SpotStrategy"); strategy != "" && strategy != "NoSpot" {
			return nil, false
		}
		reason = "stopped_subscription"
	case "PostPaid":
		if kind != "ecs" {
			return nil, false
		}
		switch text(row, "StoppedMode") {
		case "KeepCharging":
			reason = "stopped_keep_charging"
		case "Not-applicable":
			reason = "stopped_not_charge_exempt"
		case "StopCharging":
			return nil, true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	id := text(row, "InstanceId")
	c := idleBase(a, region, kind, id, text(row, "InstanceName"), row, at)
	c.Reason = reason
	c.InstanceIDs = []string{c.ID}
	return &c, true
}

func idleEIP(a model.Account, region string, row map[string]any, at time.Time) (*model.IdleCandidate, bool) {
	switch text(row, "Status") {
	case "InUse", "Associating", "Unassociating", "Releasing":
		return nil, true
	case "Available":
	default:
		return nil, false
	}
	if raw, exists := row["InstanceId"]; exists {
		id, ok := raw.(string)
		if !ok || id != "" {
			return nil, false
		}
	}
	c := idleBase(a, region, "eip", text(row, "AllocationId"), text(row, "Name"), row, at)
	c.Reason = "unbound_eip"
	if ip := ipAddress(text(row, "IpAddress")); ip != "" {
		c.PublicIP = &ip
	}
	return &c, true
}

func idleDisk(a model.Account, region string, row map[string]any, states map[string]string, ecsComplete bool, at time.Time) (*model.IdleCandidate, bool) {
	switch model.ReleaseStorageKind(text(row, "Category")) {
	case "local", "ephemeral":
		return nil, true
	case "cloud":
	default:
		return nil, false
	}
	rawCharge := text(row, "DiskChargeType")
	if rawCharge != "PrePaid" && rawCharge != "PostPaid" {
		return nil, false
	}
	ids, known := diskAttachments(row)
	if !known {
		return nil, false
	}
	reason := "unattached_cloud_disk"
	switch text(row, "Status") {
	case "Available":
		if len(ids) != 0 {
			return nil, false
		}
	case "In_use":
		if !ecsComplete || len(ids) == 0 {
			return nil, false
		}
		unknown := false
		for _, id := range ids {
			switch states[id] {
			case "Running", "Starting", "Stopping", "Pending":
				return nil, true
			case "Stopped":
			default:
				unknown = true
			}
		}
		if unknown {
			return nil, false
		}
		reason = "disk_on_stopped_instances"
	default:
		return nil, false
	}
	c := idleBase(a, region, "disk", text(row, "DiskId"), text(row, "DiskName"), row, at)
	c.ChargeType = map[string]string{"PrePaid": "subscription", "PostPaid": "pay_as_you_go"}[rawCharge]
	c.Reason = reason
	for _, id := range ids {
		c.InstanceIDs = append(c.InstanceIDs, resourceID(a, "ecs", region, id))
	}
	sort.Strings(c.InstanceIDs)
	if size, ok := integer(row["Size"]); ok {
		c.SizeGiB = &size
	}
	return &c, true
}

func idleObservation(a model.Account, region, kind string, source, ecs collection, at time.Time) model.IdleObservation {
	o := model.IdleObservation{Region: region, Kind: kind, QueriedAt: at}
	if source.err != nil {
		o.Error = model.Error(Code(source.err))
		return o
	}
	d := &model.IdleData{Scanned: len(source.rows), UnclassifiedIDs: []string{}, Candidates: []model.IdleCandidate{}}
	states := map[string]string{}
	if kind == "disk" && ecs.err == nil {
		for _, instance := range ecs.rows {
			states[text(instance, "InstanceId")] = text(instance, "Status")
		}
	}
	idKey := "InstanceId"
	if kind == "eip" {
		idKey = "AllocationId"
	} else if kind == "disk" {
		idKey = "DiskId"
	}
	for _, row := range source.rows {
		if actual := text(row, "RegionId"); actual != "" && actual != region {
			o.Error = model.Error("invalid_response")
			return o
		}
		var candidate *model.IdleCandidate
		var known bool
		switch kind {
		case "ecs", "swas":
			candidate, known = idleInstance(a, region, kind, row, at)
		case "disk":
			candidate, known = idleDisk(a, region, row, states, ecs.err == nil, at)
		case "eip":
			candidate, known = idleEIP(a, region, row, at)
		default:
			o.Error = model.Error("invalid_response")
			return o
		}
		if !known {
			d.UnclassifiedIDs = append(d.UnclassifiedIDs, text(row, idKey))
		} else if candidate != nil {
			d.Candidates = append(d.Candidates, *candidate)
		} else {
			d.NonCandidateIDs = append(d.NonCandidateIDs, text(row, idKey))
		}
	}
	if !d.Valid() {
		o.Error = model.Error("invalid_response")
		return o
	}
	o.Data = d
	return o
}
