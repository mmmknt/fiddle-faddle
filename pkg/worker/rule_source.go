package worker

import (
	"fmt"
	"github.com/mmmknt/fiddle-faddle/pkg/client"
)

type RuleSource struct {
	CurrentValue  int
	RequestCounts *client.RequestCountsResult
}

func (r *RuleSource) String() string {
	return fmt.Sprintf("current value: %v, requestCounts: %v", r.CurrentValue, r.RequestCounts.String())
}
