// Package traceinfo enriches stored observations; it never runs a trace or
// contacts an online IP ownership/geolocation service.
package traceinfo

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type Database interface {
	LookupBatch([]netip.Addr) (ipinfo.Batch, error)
}
type Resolver interface {
	LookupAddr(context.Context, string) ([]string, error)
}
type Enricher struct {
	Database Database
	Resolver Resolver
}

func (e Enricher) Enrich(ctx context.Context, r netprobe.TraceResult, now time.Time) (model.TraceEnrichment, error) {
	o := model.TraceEnrichment{EnrichedAt: now.UTC(), Status: "complete", Addresses: []model.TraceAddressInfo{}, Hops: []model.TraceHopInfo{}, Labels: []model.TraceNetworkLabel{}, DetourStatus: "unconfirmed", Attribution: ipinfo.AttributionNotice()}
	if len(r.Hops) > 30 {
		return o, netprobe.ErrInvalid
	}
	ips := []netip.Addr{}
	indices := map[string]int{}
	for i, h := range r.Hops {
		if h.TTL != i+1 || len(h.Samples) > 5 {
			return o, netprobe.ErrInvalid
		}
		for _, sample := range h.Samples {
			if sample.Address == nil {
				continue
			}
			ip, err := netip.ParseAddr(*sample.Address)
			if err != nil || ip.Zone() != "" || ip.Is4In6() || sample.RTTMS == nil || math.IsNaN(*sample.RTTMS) || math.IsInf(*sample.RTTMS, 0) || *sample.RTTMS < 0 {
				return o, netprobe.ErrInvalid
			}
			if _, seen := indices[ip.String()]; !seen {
				indices[ip.String()] = len(ips)
				ips = append(ips, ip)
			}
		}
	}
	if ctx.Err() != nil {
		return o, ctx.Err()
	}
	var batch ipinfo.Batch
	err := ipinfo.ErrUnavailable
	if e.Database != nil {
		batch, err = e.Database.LookupBatch(ips)
	}
	if err == nil {
		if len(batch.Records) != len(ips) {
			return o, netprobe.ErrInvalid
		}
		o.ASNDatabase, o.LocationDatabase = &batch.ASN, &batch.Location
	}
	for i, ip := range ips {
		record := ipinfo.Record{Address: ip.String(), ASNStatus: "unavailable", LocationStatus: "unavailable"}
		if !netprobe.PublicAddress(ip) {
			record.ASNStatus, record.LocationStatus = "not_public", "not_public"
		} else if err == nil {
			record = batch.Records[i]
			if record.Address != ip.String() {
				return o, netprobe.ErrInvalid
			}
		}
		o.Addresses = append(o.Addresses, model.TraceAddressInfo{Record: record, PTR: model.TracePTR{Status: "not_public", Names: []string{}}})
	}
	resolver := e.Resolver
	if resolver == nil {
		resolver = &net.Resolver{PreferGo: true}
	}
	// Four workers and a shared 20s deadline bound even 150 distinct routers.
	// The Go resolver honours cancellation without invoking arbitrary commands.
	dnsCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for worker := 0; worker < min(4, len(ips)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := worker; i < len(ips); i += 4 {
				if !netprobe.PublicAddress(ips[i]) {
					continue
				}
				queryCtx, stop := context.WithTimeout(dnsCtx, 2*time.Second)
				o.Addresses[i].PTR = reverse(queryCtx, resolver, ips[i].String())
				stop()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return o, ctx.Err()
	}
	for _, record := range o.Addresses {
		if record.ASNStatus == "unavailable" || record.LocationStatus == "unavailable" || record.PTR.Status == "timeout" || record.PTR.Status == "unavailable" || record.PTR.Status == "invalid_response" {
			o.Status = "partial"
		}
	}
	for _, h := range r.Hops {
		hop := model.TraceHopInfo{TTL: h.TTL, Attempts: len(h.Samples), Addresses: []string{}, ASNCandidates: []uint32{}, CountryCodes: []string{}}
		var sum float64
		for _, sample := range h.Samples {
			if sample.Address == nil {
				continue
			}
			ip, _ := netip.ParseAddr(*sample.Address)
			a := o.Addresses[indices[ip.String()]]
			hop.Responses++
			if !slices.Contains(hop.Addresses, a.Address) {
				hop.Addresses = append(hop.Addresses, a.Address)
			}
			sum += *sample.RTTMS
			if hop.MinimumMS == nil || *sample.RTTMS < *hop.MinimumMS {
				hop.MinimumMS = new(*sample.RTTMS)
			}
			if hop.MaximumMS == nil || *sample.RTTMS > *hop.MaximumMS {
				hop.MaximumMS = new(*sample.RTTMS)
			}
			if a.ASN == nil || a.ASNStatus != "ok" {
				hop.UnknownASN = true
			} else {
				if !slices.Contains(hop.ASNCandidates, a.ASN.Number) {
					hop.ASNCandidates = append(hop.ASNCandidates, a.ASN.Number)
				}
				appendLabel(&o.Labels, a.ASN.Number, a.Address, h.TTL)
			}
			if a.Location == nil || a.LocationStatus != "ok" || a.Location.CountryCode == nil {
				hop.UnknownLocation = true
			} else if !slices.Contains(hop.CountryCodes, *a.Location.CountryCode) {
				hop.CountryCodes = append(hop.CountryCodes, *a.Location.CountryCode)
			}
		}
		if hop.Attempts > 0 {
			hop.NoResponsePercent = new(100 * float64(hop.Attempts-hop.Responses) / float64(hop.Attempts))
		}
		if hop.Responses > 0 {
			hop.AverageMS = new(sum / float64(hop.Responses))
		}
		if hop.Responses == 0 || hop.Responses < hop.Attempts {
			hop.UnknownASN, hop.UnknownLocation = true, true
		}
		slices.Sort(hop.ASNCandidates)
		slices.Sort(hop.CountryCodes)
		o.Hops = append(o.Hops, hop)
	}
	return o, nil
}

func reverse(ctx context.Context, resolver Resolver, address string) model.TracePTR {
	p := model.TracePTR{Names: []string{}}
	if ctx.Err() != nil {
		p.Status = "timeout"
		return p
	}
	names, err := resolver.LookupAddr(ctx, address)
	if err != nil {
		var dns *net.DNSError
		switch {
		case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
			p.Status = "timeout"
		case errors.As(err, &dns) && dns.IsTimeout:
			p.Status = "timeout"
		case errors.As(err, &dns) && dns.IsNotFound:
			p.Status = "not_found"
		default:
			p.Status = "unavailable"
		}
		return p
	}
	p.Status = "not_found"
	invalid := false
	for _, name := range names {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		if !validName(name) {
			invalid = true
			continue
		}
		if slices.Contains(p.Names, name) {
			continue
		}
		if len(p.Names) == 8 {
			p.Truncated = true
			continue
		}
		p.Names = append(p.Names, name)
	}
	slices.Sort(p.Names)
	if len(p.Names) > 0 {
		p.Status = "ok"
	}
	if invalid {
		p.Status = "invalid_response"
	}
	return p
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func appendLabel(labels *[]model.TraceNetworkLabel, asn uint32, address string, ttl int) {
	code := map[uint32]string{4809: "cn2", 4134: "chinanet_163", 9929: "cu_9929", 4837: "cu_4837", 58453: "cmi", 45102: "alibaba_network"}[asn]
	if code == "" {
		return
	}
	index := -1
	for i := range *labels {
		if (*labels)[i].ASN == asn {
			index = i
			break
		}
	}
	if index < 0 {
		v := model.TraceNetworkLabel{Code: code, ASN: asn, Addresses: []string{}, TTLs: []int{}, ReferenceURL: "https://rdap.apnic.net/autnum/" + strconv.FormatUint(uint64(asn), 10)}
		if asn == 4809 {
			v.ServiceTier = new("unconfirmed")
		}
		if asn == 45102 {
			v.CrossBorder = new("unconfirmed")
		}
		index = len(*labels)
		*labels = append(*labels, v)
	}
	v := &(*labels)[index]
	if !slices.Contains(v.Addresses, address) {
		v.Addresses = append(v.Addresses, address)
	}
	if !slices.Contains(v.TTLs, ttl) {
		v.TTLs = append(v.TTLs, ttl)
	}
}
