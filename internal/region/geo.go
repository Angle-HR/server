package region

import (
	"net"
	"strings"

	"github.com/oschwald/geoip2-golang"
)

var countryToRegion = map[string]Region{
	// uk
	"GB": RegionUK,
	// us
	"US": RegionUS,
	"CA": RegionUS,
	// africa
	"NG": RegionAfrica,
	"GH": RegionAfrica,
	"KE": RegionAfrica,
	"ZA": RegionAfrica,
	"EG": RegionAfrica,
	"ET": RegionAfrica,
	"TZ": RegionAfrica,
	"UG": RegionAfrica,
	"SN": RegionAfrica,
	"CI": RegionAfrica,
	"CM": RegionAfrica,
	"RW": RegionAfrica,
	"ZM": RegionAfrica,
	"MW": RegionAfrica,
	"MZ": RegionAfrica,
	"AO": RegionAfrica,
	"ZW": RegionAfrica,
	"BW": RegionAfrica,
	// asia
	"IN": RegionAsia,
	// eu
	"DE": RegionEU,
	"FR": RegionEU,
	"NL": RegionEU,
	"SE": RegionEU,
	"NO": RegionEU,
	"DK": RegionEU,
	"FI": RegionEU,
	"PL": RegionEU,
	"IT": RegionEU,
	"ES": RegionEU,
	"PT": RegionEU,
	"BE": RegionEU,
	"AT": RegionEU,
	"CH": RegionEU,
	"IE": RegionEU,
	"CZ": RegionEU,
	"RO": RegionEU,
	"HU": RegionEU,
	"GR": RegionEU,
}

// regionFromCountry maps an ISO 3166-1 alpha-2 country code to a region.
func regionFromCountry(iso string) (Region, bool) {
	region, ok := countryToRegion[strings.ToUpper(iso)]
	if !ok || !Valid(region) {
		return RegionUnknown, false
	}

	return region, true
}

// lookupRegionFromIP resolves a region from a client IP using a GeoLite2 Country database.
func lookupRegionFromIP(reader *geoip2.Reader, ip string) (Region, bool) {
	if reader == nil || ip == "" {
		return RegionUnknown, false
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return RegionUnknown, false
	}

	record, err := reader.Country(parsed)
	if err != nil || record == nil {
		return RegionUnknown, false
	}

	return regionFromCountry(record.Country.IsoCode)
}
