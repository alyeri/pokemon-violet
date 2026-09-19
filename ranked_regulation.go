package main

import (
	"fmt"
	"os"
	"time"
)

// Violet 3.0.1 consumes application_data.Regulation as Value.bytes_value,
// not a preset number or base64 string. Read the user's local main image;
// do not redistribute the game's embedded regulation records.
func marshalRankedCompetitionRegulated(name string, kind uint64, now time.Time) ([]byte, error) {
	preset, ruleID, minimum := int64(3), byte(2), byte(3)
	if kind == 4 {
		preset, ruleID, minimum = 6, 5, 4
	} else if kind != 3 {
		return nil, fmt.Errorf("unsupported ranked type %d", kind)
	}
	f, err := os.Open(os.Getenv("VIOLET_RANKED_MAIN_IMAGE"))
	if err != nil {
		return nil, fmt.Errorf("open local Violet 3.0.1 main image: %w", err)
	}
	defer f.Close()
	record := make([]byte, 0x29c0)
	if _, err := f.ReadAt(record, 0x3c318d4+(preset-1)*0x29c0); err != nil {
		return nil, fmt.Errorf("read ranked preset: %w", err)
	}
	if record[0] != ruleID || record[1] != minimum || record[2] != 6 || record[3] != minimum || record[4] != minimum {
		return nil, fmt.Errorf("unexpected Violet ranked preset header")
	}
	value := appendBytesField(nil, 8, record)
	entry := appendStringField(nil, 1, "Regulation")
	entry = appendBytesField(entry, 2, value)
	applicationData := appendBytesField(nil, 1, entry)
	return appendBytesField(marshalRankedCompetitionScheduled(name, kind, now), 24, applicationData), nil
}
