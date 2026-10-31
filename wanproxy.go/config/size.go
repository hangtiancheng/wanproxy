package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseSize parses the C++ ConfigTypeSize format: a number with an
// optional b/kb/mb/gb/tb suffix (case-insensitive), base 1024.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	mult := int64(1)
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "tb"):
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(lower, "gb"):
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(lower, "mb"):
		mult = 1024 * 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(lower, "kb"):
		mult = 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(lower, "t"):
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(lower, "g"):
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(lower, "m"):
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(lower, "k"):
		mult = 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(lower, "b"):
		s = s[:len(s)-1]
	}

	// The C++ type parses with base 0, accepting 0x prefixes.
	n, err := strconv.ParseInt(strings.TrimSpace(s), 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n < 0 || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q is negative or overflows int64", s)
	}
	return n * mult, nil
}
