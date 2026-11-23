package aliyun

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var eventIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

// SystemEvents queries two distinct sources in each enabled region. CMS rows
// from other regions are ignored even if the regional endpoint returns them.
// A failed or partial source never publishes its partially collected records.
func (c Inventory) SystemEvents(ctx context.Context, a model.Account, credential model.Credential, before func() error) []model.SystemEventObservation {
	cred := Credentials{Credential: credential, Site: a.Site, Regions: a.RegionIDs}
	observations := []model.SystemEventObservation{}
	for _, region := range a.RegionIDs {
		for _, source := range []string{"ecs_history", "cms_spot"} {
			if ctx.Err() != nil {
				return observations
			}
			events, err := c.systemEventsSource(ctx, cred, region, source, before)
			o := model.SystemEventObservation{AccountID: a.ID, RegionID: region, Source: source, FetchedAt: time.Now().UTC(), Events: events}
			if err != nil {
				o.Events = nil
				o.Error = model.Error(Code(err))
			}
			observations = append(observations, o)
		}
	}
	return observations
}

func (c Inventory) systemEventsSource(ctx context.Context, cred Credentials, region, source string, before func() error) ([]model.CloudSystemEvent, error) {
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	action, path := "DescribeInstanceHistoryEvents", "InstanceSystemEventSet.InstanceSystemEventType"
	q := map[string]string{"RegionId": region, "ResourceType": "instance", "MaxResults": "100"}
	if source == "ecs_history" {
		for i, state := range []string{"Scheduled", "Executing", "Inquiring", "Executed", "Canceled", "Avoided", "Failed"} {
			q["InstanceEventCycleStatus."+strconv.Itoa(i+1)] = state
		}
	} else if source == "cms_spot" {
		action, path = "DescribeSystemEventAttribute", "SystemEvents.SystemEvent"
		q = map[string]string{"Product": "ECS", "Name": "Instance:PreemptibleInstanceInterruption", "StartTime": strconv.FormatInt(start.UnixMilli(), 10), "EndTime": strconv.FormatInt(end.UnixMilli(), 10), "PageSize": "100"}
	} else {
		return nil, failure("invalid_event_source")
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events := []model.CloudSystemEvent{}
	seen := map[string]bool{}
	tokens := map[string]bool{}
	for page := 1; page <= 50; page++ {
		if err := before(); err != nil {
			return nil, failure("account_changed")
		}
		if source == "cms_spot" {
			q["PageNumber"] = strconv.Itoa(page)
		}
		body, err := c.Caller.Call(readCtx, cred, action, region, q)
		if err != nil {
			return nil, err
		}
		if source == "cms_spot" {
			if v, ok := body["Success"]; ok && v != true && v != "true" {
				return nil, failure("invalid_response")
			}
			if v, ok := body["Code"]; ok && v != "200" && v != 200 && v != float64(200) {
				return nil, failure("invalid_response")
			}
		}
		raw, exists := valueAt(body, path)
		rows, valid := objects(raw)
		if !exists || !valid || len(rows) > 100 {
			return nil, failure("invalid_response")
		}
		for _, row := range rows {
			var event model.CloudSystemEvent
			var include bool
			if source == "ecs_history" {
				event, err = parseECSEvent(row)
				include = true
			} else {
				event, include, err = parseCMSInterruption(row, region, start, end)
			}
			if err != nil {
				return nil, err
			}
			if !include {
				continue
			}
			if seen[event.CloudEventID] {
				return nil, failure("pagination_incomplete")
			}
			seen[event.CloudEventID] = true
			events = append(events, event)
		}
		if source == "cms_spot" {
			if len(rows) < 100 {
				return events, nil
			}
			continue
		}
		token := ""
		if value, exists := body["NextToken"]; exists && value != nil {
			token, valid = value.(string)
			if !valid || len(token) > 4096 {
				return nil, failure("invalid_response")
			}
		}
		// TotalCount is documented as meaningless for token pagination.
		if token == "" {
			return events, nil
		}
		if len(rows) == 0 || tokens[token] {
			return nil, failure("pagination_incomplete")
		}
		tokens[token] = true
		q["NextToken"] = token
	}
	return nil, failure("page_limit_reached")
}

func eventTime(row map[string]any, key string) (*time.Time, error) {
	value, exists := row[key]
	if !exists || value == nil || value == "" {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, failure("invalid_response")
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, failure("invalid_response")
	}
	at = at.UTC()
	return &at, nil
}
func eventCode(row map[string]any, key string) (*int64, error) {
	raw, exists := row[key]
	if !exists || raw == nil {
		return nil, nil
	}
	n, ok := integer(raw)
	if !ok || n > 1<<31-1 {
		return nil, failure("invalid_response")
	}
	return &n, nil
}
func parseECSEvent(row map[string]any) (model.CloudSystemEvent, error) {
	e := model.CloudSystemEvent{CloudEventID: text(row, "EventId"), CloudInstanceID: text(row, "InstanceId"), EventType: text(child(row, "EventType"), "Name"), ProviderStatus: text(child(row, "EventCycleStatus"), "Name")}
	if !eventIdentifier.MatchString(e.CloudEventID) || !powerIDPattern.MatchString(e.CloudInstanceID) || !eventIdentifier.MatchString(e.EventType) || !eventIdentifier.MatchString(e.ProviderStatus) {
		return e, failure("invalid_response")
	}
	if kind, exists := row["ResourceType"]; exists && kind != "instance" {
		return e, failure("invalid_response")
	}
	var err error
	if e.EventTypeCode, err = eventCode(child(row, "EventType"), "Code"); err != nil {
		return e, err
	}
	if e.ProviderStatusCode, err = eventCode(child(row, "EventCycleStatus"), "Code"); err != nil {
		return e, err
	}
	if e.PublishedAt, err = eventTime(row, "EventPublishTime"); err != nil {
		return e, err
	}
	if e.NotBefore, err = eventTime(row, "NotBefore"); err != nil {
		return e, err
	}
	if e.FinishedAt, err = eventTime(row, "EventFinishTime"); err != nil {
		return e, err
	}
	e.Category = "other"
	if strings.HasPrefix(strings.TrimPrefix(e.EventType, "Instance:"), "SystemMaintenance.") {
		e.Category = "maintenance"
	}
	if e.EventType == "Instance:PreemptibleInstanceInterruption" {
		e.Category = "spot_interruption"
	}
	return e, nil
}
func parseCMSInterruption(row map[string]any, region string, start, end time.Time) (model.CloudSystemEvent, bool, error) {
	e := model.CloudSystemEvent{CloudEventID: text(row, "Id"), EventType: text(row, "Name"), ProviderStatus: text(row, "Status"), Category: "spot_interruption"}
	if row["Product"] != "ECS" || e.EventType != "Instance:PreemptibleInstanceInterruption" || !eventIdentifier.MatchString(e.CloudEventID) || !eventIdentifier.MatchString(e.ProviderStatus) {
		return e, false, failure("invalid_response")
	}
	eventRegion := text(row, "RegionId")
	if !model.RegionPattern.MatchString(eventRegion) {
		return e, false, failure("invalid_response")
	}
	if eventRegion != region {
		return e, false, nil
	}
	id := text(row, "ResourceId")
	if strings.HasPrefix(id, "acs:") {
		parts := strings.SplitN(id, ":", 5)
		if len(parts) != 5 || parts[0] != "acs" || parts[1] != "ecs" || parts[2] != region || parts[3] == "" || !strings.HasPrefix(parts[4], "instance/") {
			return e, false, failure("invalid_response")
		}
		id = strings.TrimPrefix(parts[4], "instance/")
	}
	if !strings.HasPrefix(id, "i-") || !powerIDPattern.MatchString(id) {
		return e, false, failure("invalid_response")
	}
	millis, ok := integer(row["Time"])
	if !ok {
		return e, false, failure("invalid_response")
	}
	at := time.UnixMilli(millis).UTC()
	if at.Before(start) || at.After(end) {
		return e, false, failure("invalid_response")
	}
	e.CloudInstanceID = id
	e.PublishedAt = &at
	return e, true, nil
}
