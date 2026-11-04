package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var sizePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)([KkMmGgTt]?[Bb])?$`)

// ParseSize parses decimal bytes with an optional B/KB/MB/GB/TB suffix.
// Units use powers of 1024. Values must fit a nonnegative int64.
func ParseSize(s string) (int64, error) {
	parts := sizePattern.FindStringSubmatch(s)
	if parts == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q overflows int64", s)
	}
	mult := int64(1)
	switch strings.ToLower(parts[2]) {
	case "kb":
		mult = 1 << 10
	case "mb":
		mult = 1 << 20
	case "gb":
		mult = 1 << 30
	case "tb":
		mult = 1 << 40
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q overflows int64", s)
	}
	return n * mult, nil
}
