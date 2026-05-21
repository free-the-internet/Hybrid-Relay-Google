package transport

import (
	"strconv"
	"strings"
)

const ackMarkerPrefix = "__fdack:"

func makeAckMarker(nextSeq uint64) string {
	return ackMarkerPrefix + strconv.FormatUint(nextSeq, 10)
}

func parseAckMarker(target string) (uint64, bool) {
	if !strings.HasPrefix(target, ackMarkerPrefix) {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(target, ackMarkerPrefix), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func isAckMarker(target string) bool {
	_, ok := parseAckMarker(target)
	return ok
}
