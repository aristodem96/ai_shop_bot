package domain

import (
	"errors"
	"fmt"
	"strings"
)

// VIN is what can be read from a vehicle identification number without a
// paid decoding database: the manufacturer from the first three characters
// (the WMI) and the model year from the tenth.
//
// Model and engine are not encoded in a standard way -- every maker uses the
// middle section differently -- so they are not guessed. The consultant uses
// the make and year to narrow the vehicle list and asks for the rest.
type VIN struct {
	Code string
	WMI  string
	// Make is empty for a manufacturer code the shop does not know.
	Make string
	// ModelYears are the years the tenth character can stand for, latest
	// first. The code repeats every 30 years, so "D" is 2013 or 1983.
	ModelYears []int
}

// ErrInvalidVIN means the input cannot be a VIN.
var ErrInvalidVIN = errors.New("invalid VIN")

// wmiMakes maps world manufacturer identifiers to the makes the shop lists.
// Several plants build one make: a Tucson from the Czech plant starts with
// TMA, one from Korea with KMH.
var wmiMakes = map[string]string{
	"TMB": "Skoda",
	"WVW": "Volkswagen", "WV1": "Volkswagen", "WV2": "Volkswagen", "WVG": "Volkswagen",
	"JTD": "Toyota", "JTN": "Toyota", "JTM": "Toyota", "JTE": "Toyota", "SB1": "Toyota", "NMT": "Toyota", "4T1": "Toyota",
	"VF1": "Renault", "UU1": "Renault", "X7L": "Renault",
	"KMH": "Hyundai", "TMA": "Hyundai", "KM8": "Hyundai",
	"KNA": "Kia", "KNE": "Kia", "KND": "Kia", "U5Y": "Kia", "U6Y": "Kia",
	"WF0": "Ford", "1FA": "Ford", "X9F": "Ford",
	// Lanos was built by Daewoo in Korea and by ZAZ in Ukraine.
	"KLA": "Daewoo", "Y6D": "Daewoo",
}

// yearCodes is the order of the tenth-character codes from 1980 (and 2010):
// letters without I, O, Q, U and Z, then the digits 1-9.
const yearCodes = "ABCDEFGHJKLMNPRSTVWXY123456789"

// ParseVIN reads a VIN in any common spelling ("tmb jg7ne 0 d0123456").
// latestYear bounds the model years offered: none after it.
func ParseVIN(s string, latestYear int) (*VIN, error) {
	code := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(s)))
	if len(code) != 17 {
		return nil, fmt.Errorf("%w: %d characters, a VIN has 17", ErrInvalidVIN, len(code))
	}
	for _, r := range code {
		switch {
		case r == 'I' || r == 'O' || r == 'Q':
			return nil, fmt.Errorf("%w: %q never appears in a VIN", ErrInvalidVIN, r)
		case (r < 'A' || r > 'Z') && (r < '0' || r > '9'):
			return nil, fmt.Errorf("%w: %q is not a letter or a digit", ErrInvalidVIN, r)
		}
	}

	v := &VIN{Code: code, WMI: code[:3], Make: wmiMakes[code[:3]]}
	if i := strings.IndexByte(yearCodes, code[9]); i >= 0 {
		for year := 1980 + i; year <= latestYear; year += 30 {
			v.ModelYears = append([]int{year}, v.ModelYears...)
		}
	}
	return v, nil
}
