package aliyun

import "github.com/JasperXzy/harbormaster/internal/model"

// The displayed IPv4 list can be partial during topology failures. Only a
// complete source set is allowed to replace an event comparison baseline.
func attachIPv4Quality(snapshots []model.Snapshot, ecs, eni, eips, swas collection) {
	known := map[string]bool{}
	topologyOK := eni.err == nil && eips.err == nil
	for _, row := range eips.rows {
		if ipAddress(text(row, "IpAddress")) == "" {
			topologyOK = false
		}
	}
	for _, row := range ecs.rows {
		v, exists := valueAt(row, "PublicIpAddress.IpAddress")
		addresses, valid := v.([]any)
		complete := topologyOK && exists && valid
		if embedded := child(row, "EipAddress"); text(embedded, "AllocationId") != "" && ipAddress(text(embedded, "IpAddress")) == "" {
			complete = false
		}
		for _, value := range addresses {
			address, _ := value.(string)
			if ipAddress(address) == "" {
				complete = false
			}
		}
		known["ecs:"+text(row, "InstanceId")] = complete
	}
	for _, row := range swas.rows {
		ip, exists := row["PublicIpAddress"].(string)
		complete := exists && (ip == "" || ipAddress(ip) != "")
		if raw, exists := row["NetworkAttributes"]; exists {
			items, valid := objects(raw)
			complete = complete && valid
			for _, item := range items {
				ip, valid := item["PublicIpAddress"].(string)
				if !valid || (ip != "" && ipAddress(ip) == "") {
					complete = false
				}
			}
		}
		known["swas:"+text(row, "InstanceId")] = complete
	}
	for index := range snapshots {
		snapshot := &snapshots[index]
		snapshot.IPv4Complete = known[snapshot.Kind+":"+text(snapshot.Payload, "cloud_instance_id")]
	}
}
