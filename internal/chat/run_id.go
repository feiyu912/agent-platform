package chat

import (
	"strconv"
	"strings"
	"time"
)

func NewRunID() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 36)
}

func ParseRunIDMillis(runID string) (int64, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0, false
	}
	if millis, err := strconv.ParseInt(runID, 36, 64); err == nil {
		return millis, true
	}
	return 0, false
}

func RunIDAfter(runID string, cursor string) bool {
	runMillis, runOK := ParseRunIDMillis(runID)
	cursorMillis, cursorOK := ParseRunIDMillis(cursor)
	switch {
	case runOK && cursorOK:
		if runMillis != cursorMillis {
			return runMillis > cursorMillis
		}
		return strings.Compare(runID, cursor) > 0
	case runOK != cursorOK:
		return strings.Compare(runID, cursor) > 0
	default:
		return strings.Compare(runID, cursor) > 0
	}
}
