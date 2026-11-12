// Package ipinfo reads bundled or downloaded DB-IP Lite databases offline.
// No lookup performs HTTP or DNS. Optional monthly updates are separate.
package ipinfo

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/oschwald/maxminddb-golang/v2"
)

//go:embed data/*.gz data/manifest.json data/LICENSE.md
var bundle embed.FS

var ErrUnavailable = errors.New("ipdb_unavailable")
var ErrUpdate = errors.New("ipdb_update_failed")

const maxRawBytes = 256 << 20

type Attribution struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	License    string `json:"license"`
	LicenseURL string `json:"license_url"`
}

func AttributionNotice() []Attribution {
	return []Attribution{{Name: "IP Geolocation by DB-IP", URL: "https://db-ip.com", License: "CC BY 4.0", LicenseURL: "https://creativecommons.org/licenses/by/4.0/"}}
}

type Dataset struct {
	Name      string    `json:"name"`
	Edition   string    `json:"edition"`
	BuiltAt   time.Time `json:"built_at"`
	SizeBytes int64     `json:"size_bytes"`
	Origin    string    `json:"origin"`
	Storage   string    `json:"storage"`
	SourceURL string    `json:"source_url"`
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Status struct {
	Mode                string        `json:"mode"`
	CityEnabled         bool          `json:"city_enabled"`
	AutoUpdate          bool          `json:"auto_update"`
	ASN                 Dataset       `json:"asn"`
	Country             Dataset       `json:"country"`
	City                *Dataset      `json:"city"`
	LastCheckAt         *time.Time    `json:"last_check_at"`
	LastUpdateAt        *time.Time    `json:"last_update_at"`
	LastError           *Failure      `json:"last_error"`
	Attribution         []Attribution `json:"attribution"`
	GoHeapAllocBytes    uint64        `json:"go_heap_alloc_bytes"`
	ProcessPeakRSSBytes *uint64       `json:"process_peak_rss_bytes"`
	MappedFileBytes     int64         `json:"mapped_file_bytes"`
}
type Options struct {
	Directory               string
	CityEnabled, AutoUpdate bool
	HTTP                    *http.Client
}
type database struct {
	reader *maxminddb.Reader
	info   Dataset
	path   string
}
type Manager struct {
	mu               sync.RWMutex
	updateMu         sync.Mutex
	options          Options
	dbs              map[string]*database
	checked, updated *time.Time
	lastError        *Failure
	closed           bool
}
type bundleFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

var builtins struct {
	once sync.Once
	data map[string][]byte
	err  error
}

func loadBundle() (map[string][]byte, error) {
	builtins.once.Do(func() {
		var manifest struct {
			Files map[string]bundleFile `json:"files"`
		}
		raw, err := bundle.ReadFile("data/manifest.json")
		if err != nil {
			builtins.err = ErrUnavailable
			return
		}
		if json.Unmarshal(raw, &manifest) != nil {
			builtins.err = ErrUnavailable
			return
		}
		builtins.data = map[string][]byte{}
		for _, kind := range []string{"asn", "country"} {
			file := manifest.Files[kind]
			packed, err := bundle.ReadFile("data/" + file.Name)
			if err != nil {
				builtins.err = ErrUnavailable
				return
			}
			z, err := gzip.NewReader(bytes.NewReader(packed))
			if err != nil {
				builtins.err = ErrUnavailable
				return
			}
			// Exact allocation avoids a growing buffer retaining multiple copies.
			if file.Size < 1 || file.Size > maxRawBytes {
				z.Close()
				builtins.err = ErrUnavailable
				return
			}
			data := make([]byte, int(file.Size))
			_, err = io.ReadFull(z, data)
			var extra [1]byte
			n, end := z.Read(extra[:])
			z.Close()
			sum := sha256.Sum256(data)
			if err != nil || n != 0 || end != io.EOF || hex.EncodeToString(sum[:]) != file.SHA256 {
				builtins.err = ErrUnavailable
				return
			}
			r, err := maxminddb.OpenBytes(data, maxminddb.DisableStringCache())
			if err != nil {
				builtins.err = ErrUnavailable
				return
			}
			if validate(kind, r, time.Time{}) != nil || r.Verify() != nil {
				r.Close()
				builtins.err = ErrUnavailable
				return
			}
			r.Close()
			builtins.data[kind] = data
		}
	})
	return builtins.data, builtins.err
}
func validate(kind string, r *maxminddb.Reader, now time.Time) error {
	name := map[string]string{"asn": "ASN", "country": "Country", "city": "City"}[kind]
	if name == "" || !strings.HasPrefix(r.Metadata.DatabaseType, "DBIP-"+name+"-Lite") || r.Metadata.IPVersion != 6 || r.Metadata.BinaryFormatMajorVersion != 2 || r.Metadata.NodeCount == 0 {
		return ErrUnavailable
	}
	at := r.Metadata.BuildTime()
	if at.Year() < 2000 || at.Year() > 2100 || !now.IsZero() && at.After(now.Add(48*time.Hour)) {
		return ErrUnavailable
	}
	return nil
}
func sourceURL(kind, edition string) string {
	return "https://download.db-ip.com/free/dbip-" + kind + "-lite-" + edition + ".mmdb.gz"
}
func info(kind string, r *maxminddb.Reader, size int64, origin, storage string) Dataset {
	at := r.Metadata.BuildTime().UTC()
	edition := at.Format("2006-01")
	return Dataset{Name: r.Metadata.DatabaseType, Edition: edition, BuiltAt: at, SizeBytes: size, Origin: origin, Storage: storage, SourceURL: sourceURL(kind, edition)}
}
func New(options Options) (*Manager, error) {
	data, err := loadBundle()
	if err != nil {
		return nil, err
	}
	m := &Manager{options: options, dbs: map[string]*database{}}
	for _, kind := range []string{"asn", "country"} {
		r, err := maxminddb.OpenBytes(data[kind], maxminddb.DisableStringCache())
		if err != nil {
			m.Close()
			return nil, ErrUnavailable
		}
		m.dbs[kind] = &database{reader: r, info: info(kind, r, int64(len(data[kind])), "bundled", "memory")}
	}
	if options.Directory != "" {
		if err = m.restore(); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.lastError = &Failure{Code: ErrUpdate.Error(), Message: ErrUpdate.Error()}
		}
	}
	return m, nil
}
func (m *Manager) Close() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		for _, db := range m.dbs {
			db.reader.Close()
		}
		m.closed = true
	}
	return nil
}
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := Status{Mode: "country", CityEnabled: m.options.CityEnabled, AutoUpdate: m.options.AutoUpdate, ASN: m.dbs["asn"].info, Country: m.dbs["country"].info, Attribution: AttributionNotice()}
	if city := m.dbs["city"]; city != nil {
		v.Mode = "city"
		value := city.info
		v.City = &value
	}
	if m.checked != nil {
		value := *m.checked
		v.LastCheckAt = &value
	}
	if m.updated != nil {
		value := *m.updated
		v.LastUpdateAt = &value
	}
	if m.lastError != nil {
		value := *m.lastError
		v.LastError = &value
	}
	for _, db := range m.dbs {
		if db.info.Storage == "mmap" {
			v.MappedFileBytes += db.info.SizeBytes
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	v.GoHeapAllocBytes = mem.HeapAlloc
	v.ProcessPeakRSSBytes = peakRSS()
	return v
}

type ASN struct {
	Number       uint32  `json:"number"`
	Organization *string `json:"organization"`
	Prefix       string  `json:"prefix"`
	Dataset      Dataset `json:"dataset"`
}
type Location struct {
	CountryCode *string  `json:"country_code"`
	CountryName *string  `json:"country_name"`
	Region      *string  `json:"region"`
	City        *string  `json:"city"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Prefix      string   `json:"prefix"`
	Granularity string   `json:"granularity"`
	Dataset     Dataset  `json:"dataset"`
}
type Record struct {
	Address        string    `json:"address"`
	ASN            *ASN      `json:"asn"`
	Location       *Location `json:"location"`
	ASNStatus      string    `json:"asn_status"`
	LocationStatus string    `json:"location_status"`
}
type names struct {
	English string `maxminddb:"en,maxsize:512"`
}
type geoRecord struct {
	Country struct {
		Code  string `maxminddb:"iso_code,maxsize:2"`
		Names names  `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names names `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		Names names `maxminddb:"names"`
	} `maxminddb:"subdivisions,maxsize:8"`
	Location struct {
		Latitude  *float64 `maxminddb:"latitude"`
		Longitude *float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	owned := strings.Clone(s)
	return &owned
}

func (m *Manager) Lookup(ip netip.Addr) Record {
	v := Record{Address: ip.String(), ASNStatus: "not_public", LocationStatus: "not_public"}
	if !netprobe.PublicAddress(ip) {
		return v
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v.ASNStatus, v.LocationStatus = "not_found", "not_found"
	if m.closed {
		v.ASNStatus, v.LocationStatus = "unavailable", "unavailable"
		return v
	}
	asn := m.dbs["asn"]
	result := asn.reader.Lookup(ip)
	var a struct {
		Number       uint32 `maxminddb:"autonomous_system_number"`
		Organization string `maxminddb:"autonomous_system_organization,maxsize:512"`
	}
	if err := result.Decode(&a); err != nil {
		v.ASNStatus = "unavailable"
	} else if result.Found() && a.Number != 0 {
		v.ASNStatus = "ok"
		v.ASN = &ASN{Number: a.Number, Organization: optional(a.Organization), Prefix: result.Prefix().String(), Dataset: asn.info}
	}
	geo := m.dbs["country"]
	granularity := "country"
	if city := m.dbs["city"]; city != nil {
		geo = city
		granularity = "city"
	}
	result = geo.reader.Lookup(ip)
	var g geoRecord
	if err := result.Decode(&g); err != nil {
		v.LocationStatus = "unavailable"
		return v
	}
	if !result.Found() {
		return v
	}
	location := &Location{CountryCode: optional(g.Country.Code), CountryName: optional(g.Country.Names.English), Prefix: result.Prefix().String(), Granularity: granularity, Dataset: geo.info}
	if granularity == "city" {
		location.City = optional(g.City.Names.English)
		if len(g.Subdivisions) > 0 {
			location.Region = optional(g.Subdivisions[0].Names.English)
		}
		lat, lon := g.Location.Latitude, g.Location.Longitude
		if lat != nil && lon != nil && !math.IsNaN(*lat) && !math.IsNaN(*lon) && !math.IsInf(*lat, 0) && !math.IsInf(*lon, 0) && *lat >= -90 && *lat <= 90 && *lon >= -180 && *lon <= 180 {
			location.Latitude = new(*lat)
			location.Longitude = new(*lon)
		}
	}
	if location.CountryCode != nil || location.City != nil || location.Region != nil {
		v.LocationStatus = "ok"
		v.Location = location
	}
	return v
}

// cachePath accepts only immutable generation directories created by Sync.
func cachePath(dir, path string) (string, error) {
	clean := filepath.Clean(path)
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if filepath.IsAbs(path) || len(parts) != 2 || !strings.HasPrefix(parts[0], "set-") || parts[0] != filepath.Base(parts[0]) {
		return "", ErrUpdate
	}
	if parts[1] != "asn.mmdb" && parts[1] != "country.mmdb" && parts[1] != "city.mmdb" {
		return "", ErrUpdate
	}
	return filepath.Join(dir, clean), nil
}
