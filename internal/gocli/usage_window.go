package gocli

import "strings"

const (
	usageTimeBasisCumulative  = "cumulative"
	usageTimeBasisWindowDelta = "window_delta"
	usageCoverageFull         = "full"
	usageCoveragePartial      = "partial"
)

type usageReportSource struct {
	Records             []usageRecord
	DayGroups           []usageGroupRow
	SessionRootsScanned int
	TimeBasis           string
	Coverage            string
}

func loadUsageReportSource(options usageOptions) (usageReportSource, error) {
	if strings.TrimSpace(options.Since) == "" {
		records, sessionRootsScanned, err := loadUsageRecordsShared(options)
		if err != nil {
			return usageReportSource{}, err
		}
		return usageReportSource{
			Records:             records,
			DayGroups:           buildUsageGroups(records, "day"),
			SessionRootsScanned: sessionRootsScanned,
			TimeBasis:           usageTimeBasisCumulative,
			Coverage:            usageCoverageFull,
		}, nil
	}
	return loadWindowedUsageReportSource(options)
}

func loadWindowedUsageReportSource(options usageOptions) (usageReportSource, error) {
	state, err := loadUsageSharedState(options.CWD)
	if err != nil {
		return usageReportSource{}, err
	}
	return loadWindowedUsageReportSourceFromSQLite(options, state.SessionRootsScanned)
}
