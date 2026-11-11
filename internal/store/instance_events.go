package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Nil address sets mean unknown; non-nil empty sets mean confirmed absent.
type instanceEventValues struct {
	Status     string   `json:"status"`
	PublicIPv4 []string `json:"public_ipv4"`
	IPv6       []string `json:"ipv6"`
}
type instanceEventState struct {
	ID, Kind, Region, CloudID, Name string
	Present                         bool
	Values                          instanceEventValues
}

func addressSet(addresses []string, ipv6 bool) []string {
	if addresses == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, address := range addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Zone() != "" || (ipv6 && (!ip.Is6() || ip.Is4In6())) || (!ipv6 && !ip.Is4()) {
			return nil
		}
		seen[ip.String()] = true
	}
	result := make([]string, 0, len(seen))
	for ip := range seen {
		result = append(result, ip)
	}
	sort.Strings(result)
	return result
}

func eventValues(snapshot model.Snapshot) (instanceEventValues, error) {
	var data struct {
		Status    string `json:"status"`
		PublicIPs []struct {
			Address string `json:"address"`
		} `json:"public_ips"`
		IPv6 *struct {
			Status    string   `json:"status"`
			Addresses []string `json:"addresses"`
		} `json:"ipv6"`
	}
	raw, err := json.Marshal(snapshot.Payload)
	if err != nil {
		return instanceEventValues{}, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return instanceEventValues{}, err
	}
	values := instanceEventValues{}
	switch data.Status {
	case "pending", "starting", "running", "stopping", "stopped":
		values.Status = data.Status
	}
	if snapshot.IPv4Complete && data.PublicIPs != nil {
		addresses := []string{}
		for _, ip := range data.PublicIPs {
			addresses = append(addresses, ip.Address)
		}
		values.PublicIPv4 = addressSet(addresses, false)
	}
	if data.IPv6 != nil && data.IPv6.Status == "ok" {
		values.IPv6 = addressSet(data.IPv6.Addresses, true)
	}
	return values, nil
}

func recordInstanceEvents(ctx context.Context, tx *sql.Tx, account string, result model.ScanResult, now time.Time) error {
	// Existing resources establish field baselines on their next good scan.
	// Do not invent a discovery event merely because this feature was installed.
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO instance_event_state(account_id,instance_id,kind,region,cloud_id,name,present,value_json)
 SELECT account_id,id,kind,region,coalesce(json_extract(payload_json,'$.cloud_instance_id'),id),
 coalesce(json_extract(payload_json,'$.name'),id),present,'{}'
 FROM resource_snapshot WHERE account_id=? AND kind IN ('ecs','swas')`, account)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT instance_id,kind,region,cloud_id,name,present,value_json FROM instance_event_state WHERE account_id=? ORDER BY instance_id", account)
	if err != nil {
		return err
	}
	states := map[string]*instanceEventState{}
	for rows.Next() {
		state := &instanceEventState{}
		var raw string
		if err = rows.Scan(&state.ID, &state.Kind, &state.Region, &state.CloudID, &state.Name, &state.Present, &raw); err == nil {
			err = json.Unmarshal([]byte(raw), &state.Values)
		}
		if err != nil {
			rows.Close()
			return err
		}
		states[state.ID] = state
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	emit := func(state *instanceEventState, kind string, before, after map[string]any) error {
		event := model.InstanceEvent{ID: model.NewID("iev_"), AccountID: account, InstanceID: state.ID, CloudInstanceID: state.CloudID, InstanceKind: state.Kind, RegionID: state.Region, InstanceName: state.Name, Kind: kind, ObservedAt: now.UTC(), Before: before, After: after}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO instance_event(id,account_id,instance_id,payload_json) VALUES(?,?,?,?)", event.ID, account, state.ID, string(raw))
		if err != nil {
			return err
		}
		return notifyInstanceIPChange(ctx, tx, event, now)
	}
	seen := map[string]bool{}
	for _, snapshot := range result.Snapshots {
		if snapshot.Kind != "ecs" && snapshot.Kind != "swas" {
			continue
		}
		seen[snapshot.ID] = true
		current, err := eventValues(snapshot)
		if err != nil {
			return err
		}
		state, exists := states[snapshot.ID]
		if !exists {
			state = &instanceEventState{ID: snapshot.ID, Kind: snapshot.Kind, Region: snapshot.Region}
			states[state.ID] = state
		}
		state.CloudID, _ = snapshot.Payload["cloud_instance_id"].(string)
		state.Name, _ = snapshot.Payload["name"].(string)
		if !exists {
			if err = emit(state, "discovered", nil, map[string]any{"present": true}); err != nil {
				return err
			}
		} else if !state.Present {
			if err = emit(state, "reappeared", map[string]any{"present": false}, map[string]any{"present": true}); err != nil {
				return err
			}
		}
		old := state.Values
		if current.Status != "" {
			if state.Present && old.Status != "" && old.Status != current.Status {
				if err = emit(state, "status_changed", map[string]any{"status": old.Status}, map[string]any{"status": current.Status}); err != nil {
					return err
				}
			}
			state.Values.Status = current.Status
		}
		for _, change := range []struct {
			kind    string
			old     []string
			current []string
			target  *[]string
		}{
			{"public_ipv4_changed", old.PublicIPv4, current.PublicIPv4, &state.Values.PublicIPv4},
			{"ipv6_changed", old.IPv6, current.IPv6, &state.Values.IPv6},
		} {
			if change.current == nil {
				continue
			}
			if state.Present && change.old != nil && !slices.Equal(change.old, change.current) {
				if err = emit(state, change.kind, map[string]any{"addresses": change.old}, map[string]any{"addresses": change.current}); err != nil {
					return err
				}
			}
			*change.target = change.current
		}
		state.Present = true
	}
	for _, state := range states {
		if !seen[state.ID] && state.Present && result.CompleteScopes[model.Scope(state.Kind, state.Region)] {
			if err = emit(state, "disappeared", map[string]any{"present": true}, map[string]any{"present": false}); err != nil {
				return err
			}
			state.Present = false
		}
		raw, err := json.Marshal(state.Values)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO instance_event_state(account_id,instance_id,kind,region,cloud_id,name,present,value_json) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(account_id,instance_id) DO UPDATE SET cloud_id=excluded.cloud_id,name=excluded.name,present=excluded.present,value_json=excluded.value_json`, account, state.ID, state.Kind, state.Region, state.CloudID, state.Name, state.Present, string(raw))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InstanceEvents(ctx context.Context, account, instance string, before int64, limit int) ([]model.InstanceEvent, error) {
	if before < 0 || limit < 1 || limit > 201 {
		return nil, ErrNotFound
	}
	clauses := []string{"1=1"}
	args := []any{}
	if account != "" {
		clauses, args = append(clauses, "account_id=?"), append(args, account)
	}
	if instance != "" {
		clauses, args = append(clauses, "instance_id=?"), append(args, instance)
	}
	if before != 0 {
		clauses, args = append(clauses, "sequence<?"), append(args, before)
	}
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT sequence,payload_json FROM instance_event WHERE "+strings.Join(clauses, " AND ")+" ORDER BY sequence DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []model.InstanceEvent{}
	for rows.Next() {
		var raw string
		var sequence int64
		var event model.InstanceEvent
		if err := rows.Scan(&sequence, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		event.Sequence = sequence
		events = append(events, event)
	}
	return events, rows.Err()
}
