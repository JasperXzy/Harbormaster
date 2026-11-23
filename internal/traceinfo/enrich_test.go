package traceinfo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type databaseFunc func([]netip.Addr) (ipinfo.Batch, error)

func (f databaseFunc) LookupBatch(ips []netip.Addr) (ipinfo.Batch, error) { return f(ips) }

type resolverFunc func(context.Context, string) ([]string, error)

func (f resolverFunc) LookupAddr(c context.Context, ip string) ([]string, error) { return f(c, ip) }

func noPTR(context.Context, string) ([]string, error) {
	return nil, &net.DNSError{IsNotFound: true}
}
func observation(address string, seq int) netprobe.TraceSample {
	s := netprobe.TraceSample{Sequence: seq, StartedAt: time.Now().UTC()}
	if address == "" {
		s.ErrorCode = new("probe_timeout")
	} else {
		s.Address, s.RTTMS = &address, new(float64(seq))
	}
	return s
}

func TestEvidenceLabelsPreserveMultipathUnknownsAndGeographicUncertainty(t *testing.T) {
	catalog := map[string]uint32{"1.1.1.1": 4809, "8.8.8.8": 45102, "9.9.9.9": 4134, "223.5.5.5": 9929, "223.6.6.6": 4837, "114.114.114.114": 58453}
	db := databaseFunc(func(ips []netip.Addr) (ipinfo.Batch, error) {
		b := ipinfo.Batch{ASN: ipinfo.Dataset{Name: "synthetic asn", Edition: "2026-11"}, Location: ipinfo.Dataset{Name: "synthetic country", Edition: "2026-11"}, Records: []ipinfo.Record{}}
		for _, ip := range ips {
			r := ipinfo.Record{Address: ip.String(), ASNStatus: "not_found", LocationStatus: "not_found"}
			if asn := catalog[r.Address]; asn != 0 {
				r.ASNStatus, r.LocationStatus = "ok", "ok"
				r.ASN = &ipinfo.ASN{Number: asn, Dataset: b.ASN}
				r.Location = &ipinfo.Location{CountryCode: new("US"), Granularity: "country", Dataset: b.Location}
			}
			b.Records = append(b.Records, r)
		}
		return b, nil
	})
	r := netprobe.TraceResult{Hops: []netprobe.TraceHop{
		{TTL: 1, Samples: []netprobe.TraceSample{observation("1.1.1.1", 1), observation("8.8.8.8", 2), observation("", 3)}},
		{TTL: 2, Samples: []netprobe.TraceSample{observation("10.0.0.1", 4), observation("59.43.1.1", 5)}},
		{TTL: 3, Samples: []netprobe.TraceSample{observation("9.9.9.9", 6), observation("223.5.5.5", 7), observation("223.6.6.6", 8), observation("114.114.114.114", 9), observation("1.1.1.1", 10)}},
		{TTL: 4, Samples: []netprobe.TraceSample{observation("", 11)}},
		{TTL: 5, Samples: []netprobe.TraceSample{}},
	}}
	var queries sync.Map
	e := Enricher{Database: db, Resolver: resolverFunc(func(ctx context.Context, ip string) ([]string, error) {
		if ip == "10.0.0.1" {
			t.Error("private router queried")
		}
		if _, exists := queries.LoadOrStore(ip, true); exists {
			t.Error("duplicate PTR", ip)
		}
		return noPTR(ctx, ip)
	})}
	out, err := e.Enrich(context.Background(), r, time.Now())
	if err != nil || out.Status != "complete" || out.DetourStatus != "unconfirmed" || len(out.Labels) != 6 || len(out.Addresses) != 8 {
		t.Fatal(out, err)
	}
	h := out.Hops[0]
	if h.Responses != 2 || h.Attempts != 3 || *h.AverageMS != 1.5 || *h.MinimumMS != 1 || *h.MaximumMS != 2 || *h.NoResponsePercent < 33 || !h.UnknownASN || len(h.ASNCandidates) != 2 {
		t.Fatal("lost multipath or unanswered observation", h)
	}
	if !out.Hops[1].UnknownASN || len(out.Hops[1].ASNCandidates) != 0 || out.Hops[3].AverageMS != nil || *out.Hops[3].NoResponsePercent != 100 || out.Hops[4].NoResponsePercent != nil {
		t.Fatal("invented known/zero RTT", out.Hops)
	}
	for _, v := range out.Labels {
		if v.ASN == 4809 && (v.ServiceTier == nil || *v.ServiceTier != "unconfirmed" || len(v.TTLs) != 2 || len(v.Addresses) != 1) {
			t.Fatal(v)
		}
		if v.ASN == 45102 && (v.CrossBorder == nil || *v.CrossBorder != "unconfirmed") {
			t.Fatal(v)
		}
		for _, address := range v.Addresses {
			if address == "59.43.1.1" {
				t.Fatal("prefix guessed from reputation", v)
			}
		}
	}
}

func TestPTRResultsAreBoundedAndDoNotExposeResolverErrors(t *testing.T) {
	cases := []struct {
		name      string
		names     []string
		err       error
		status    string
		count     int
		truncated bool
	}{
		{"names", []string{"Router.Example.", "router.example", "other.example."}, nil, "ok", 2, false},
		{"empty", nil, nil, "not_found", 0, false},
		{"missing", nil, &net.DNSError{IsNotFound: true, Err: "secret resolver input"}, "not_found", 0, false},
		{"timeout", nil, &net.DNSError{IsTimeout: true, Err: "secret resolver input"}, "timeout", 0, false},
		{"error", nil, errors.New("secret resolver input"), "unavailable", 0, false},
		{"malformed", []string{"valid.example", "bad\nname.example", strings.Repeat("a", 64) + ".example", "-bad.example"}, nil, "invalid_response", 1, false},
		{"limit", []string{"a.example", "b.example", "c.example", "d.example", "e.example", "f.example", "g.example", "h.example", "i.example"}, nil, "ok", 8, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := reverse(context.Background(), resolverFunc(func(context.Context, string) ([]string, error) { return c.names, c.err }), "1.1.1.1")
			data, _ := json.Marshal(p)
			if p.Status != c.status || len(p.Names) != c.count || p.Truncated != c.truncated || strings.Contains(string(data), "secret") {
				t.Fatal(p, string(data))
			}
		})
	}
}

func TestPTRConcurrencyDeadlineCancellationAndDatabaseFailure(t *testing.T) {
	r := netprobe.TraceResult{Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{}}, {TTL: 2, Samples: []netprobe.TraceSample{}}}}
	for i, ip := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "223.5.5.5", "114.114.114.114", "208.67.222.222"} {
		h := min(i/5, 1)
		r.Hops[h].Samples = append(r.Hops[h].Samples, observation(ip, i+1))
	}
	var active, peak, calls atomic.Int32
	e := Enricher{Resolver: resolverFunc(func(ctx context.Context, ip string) ([]string, error) {
		calls.Add(1)
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer active.Add(-1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("unbounded PTR")
		}
		time.Sleep(5 * time.Millisecond)
		return noPTR(ctx, ip)
	})}
	out, err := e.Enrich(context.Background(), r, time.Now())
	if err != nil || out.Status != "partial" || out.ASNDatabase != nil || peak.Load() > 4 || calls.Load() != 6 {
		t.Fatal(out, err, peak.Load(), calls.Load())
	}
	e.Resolver = resolverFunc(func(ctx context.Context, ip string) ([]string, error) { <-ctx.Done(); return nil, ctx.Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err = e.Enrich(ctx, r, time.Now()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestBundledLookupEnrichmentUsesRealVersionButNoLiveDNS(t *testing.T) {
	db, err := ipinfo.New(ipinfo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := netprobe.TraceResult{Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{observation("1.1.1.1", 1), observation("59.43.1.1", 2), observation("192.0.2.1", 3)}}}}
	out, err := (Enricher{Database: db, Resolver: resolverFunc(noPTR)}).Enrich(context.Background(), r, time.Now())
	if err != nil || out.ASNDatabase.Edition != "2026-11" || out.LocationDatabase.Edition != "2026-11" || out.Addresses[0].ASN.Number != 13335 || out.Addresses[0].Location.Granularity != "country" || out.Addresses[0].Location.City != nil || out.Addresses[1].ASN != nil || out.Addresses[2].PTR.Status != "not_public" || len(out.Labels) != 0 {
		t.Fatal(out, err)
	}
	text := model.TraceText(model.TraceDetail{TraceRun: model.TraceRun{EnrichmentStatus: out.Status}, Result: &r, Enrichment: &out})
	for _, want := range []string{"2026-11", "IP Geolocation by DB-IP <https://db-ip.com>", "no_response_percent", "unconfirmed"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing export evidence", want)
		}
	}
}
