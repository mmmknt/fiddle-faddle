package worker

import (
	"fmt"
	"time"
)

type RoutingRule struct {
	version        string
	updatedAt      *time.Time
	targetHost     string
	internalWeight int
	externalWeight int
}

func (r *RoutingRule) Exist() bool {
	return r.targetHost != ""
}

func (r *RoutingRule) Equal(rule *RoutingRule) bool {
	return r.targetHost == rule.targetHost &&
		r.internalWeight == rule.internalWeight &&
		r.externalWeight == rule.externalWeight
}

func (r *RoutingRule) String() string {
	updatedAt := ""
	if r.updatedAt != nil {
		updatedAt = r.updatedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("version: %s, updatedAt: %s, targetHost: %s, internalWeight: %v, externalWeight: %v",
		r.version, updatedAt, r.targetHost, r.internalWeight, r.externalWeight)
}
