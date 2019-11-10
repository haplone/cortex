package ingester

import (
	"fmt"
	"math"

	"github.com/cortexproject/cortex/pkg/util/validation"
)

const (
	errMaxSeriesPerMetricLimitExceeded = "per-metric series limit (local: %d global: %d actual local: %d) exceeded"
	errMaxSeriesPerUserLimitExceeded   = "per-user series limit (local: %d global: %d actual local: %d) exceeded"
)

// RingCount is the interface exposed by a ring implementation which allows
// to count members
type RingCount interface {
	HealthyInstancesCount() int
}

// SeriesLimiter implements primitives to get the maximum number of series
// an ingester can handle for a specific tenant
type SeriesLimiter struct {
	limits            *validation.Overrides
	ring              RingCount
	replicationFactor int
	shardByAllLabels  bool
}

// NewSeriesLimiter makes a new in-memory series limiter
func NewSeriesLimiter(limits *validation.Overrides, ring RingCount, replicationFactor int, shardByAllLabels bool) *SeriesLimiter {
	return &SeriesLimiter{
		limits:            limits,
		ring:              ring,
		replicationFactor: replicationFactor,
		shardByAllLabels:  shardByAllLabels,
	}
}

// AssertMaxSeriesPerMetric limit has not been reached compared to the current
// number of series in input and returns an error if so.
func (l *SeriesLimiter) AssertMaxSeriesPerMetric(userID string, series int) error {
	actualLimit := l.maxSeriesPerMetric(userID)
	if series < actualLimit {
		return nil
	}

	localLimit := l.limits.MaxLocalSeriesPerMetric(userID)
	globalLimit := l.limits.MaxGlobalSeriesPerMetric(userID)

	return fmt.Errorf(errMaxSeriesPerMetricLimitExceeded, localLimit, globalLimit, actualLimit)
}

// AssertMaxSeriesPerUser limit has not been reached compared to the current
// number of series in input and returns an error if so.
func (l *SeriesLimiter) AssertMaxSeriesPerUser(userID string, series int) error {
	actualLimit := l.maxSeriesPerUser(userID)
	if series < actualLimit {
		return nil
	}

	localLimit := l.limits.MaxLocalSeriesPerUser(userID)
	globalLimit := l.limits.MaxGlobalSeriesPerUser(userID)

	return fmt.Errorf(errMaxSeriesPerUserLimitExceeded, localLimit, globalLimit, actualLimit)
}

// MaxSeriesPerQuery returns the maximum number of series a query is allowed to hit.
func (l *SeriesLimiter) MaxSeriesPerQuery(userID string) int {
	return l.limits.MaxSeriesPerQuery(userID)
}

func (l *SeriesLimiter) maxSeriesPerMetric(userID string) int {
	localLimit := l.limits.MaxLocalSeriesPerMetric(userID)
	globalLimit := l.limits.MaxGlobalSeriesPerMetric(userID)

	if globalLimit > 0 {
		if l.shardByAllLabels {
			// We can assume that series are evenly distributed across ingesters
			// so we do convert the global limit into a local limit
			localLimit = l.minNonZero(localLimit, l.convertGlobalToLocalLimit(globalLimit))
		} else {
			// Given a metric is always pushed to the same set of ingesters (based on
			// the replication factor), we can configure the per-ingester local limit
			// equal to the global limit.
			localLimit = l.minNonZero(localLimit, globalLimit)
		}
	}

	// If both the local and global limits are disabled, we just
	// use the largest int value
	if localLimit == 0 {
		localLimit = math.MaxInt32
	}

	return localLimit
}

func (l *SeriesLimiter) maxSeriesPerUser(userID string) int {
	localLimit := l.limits.MaxLocalSeriesPerUser(userID)

	// The global limit is supported only when shard-by-all-labels is enabled,
	// otherwise we wouldn't get an even split of series across ingesters and
	// can't take a "local decision" without any centralized coordination.
	if l.shardByAllLabels {
		// We can assume that series are evenly distributed across ingesters
		// so we do convert the global limit into a local limit
		globalLimit := l.limits.MaxGlobalSeriesPerUser(userID)
		localLimit = l.minNonZero(localLimit, l.convertGlobalToLocalLimit(globalLimit))
	}

	// If both the local and global limits are disabled, we just
	// use the largest int value
	if localLimit == 0 {
		localLimit = math.MaxInt32
	}

	return localLimit
}

func (l *SeriesLimiter) convertGlobalToLocalLimit(globalLimit int) int {
	if globalLimit == 0 {
		return 0
	}

	// Given we don't need a super accurate count (ie. when the ingesters
	// topology changes) and we prefer to always be in favor of the tenant,
	// we can use a per-ingester limit equal to:
	// (global limit / number of ingesters) * replication factor
	numIngesters := l.ring.HealthyInstancesCount()

	// May happen because the number of ingesters is asynchronously updated.
	// If happens, we just temporarily ignore the global limit.
	if numIngesters > 0 {
		return int((float64(globalLimit) / float64(numIngesters)) * float64(l.replicationFactor))
	}

	return 0
}

func (l *SeriesLimiter) minNonZero(first, second int) int {
	if first == 0 || (second != 0 && first > second) {
		return second
	}

	return first
}
