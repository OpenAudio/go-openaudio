package server

import "time"

const DefaultStoreRecentTTL = 365 * 24 * time.Hour

type repairRetentionPolicy struct {
	storeAll    bool
	storeRecent bool
	ttl         time.Duration
	now         time.Time
}

func newRepairRetentionPolicy(config MediorumConfig, now time.Time) repairRetentionPolicy {
	ttl := config.StoreRecentTTL
	if ttl <= 0 {
		ttl = DefaultStoreRecentTTL
	}
	return repairRetentionPolicy{
		storeAll:    config.StoreAll,
		storeRecent: config.StoreRecent,
		ttl:         ttl,
		now:         now,
	}
}

func (p repairRetentionPolicy) shouldStoreRecent(createdAt time.Time) bool {
	if p.storeAll || !p.storeRecent || createdAt.IsZero() {
		return false
	}
	return !createdAt.Before(p.now.Add(-p.ttl))
}
