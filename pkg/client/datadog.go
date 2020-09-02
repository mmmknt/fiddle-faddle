package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	dclient "github.com/DataDog/datadog-api-client-go/api/v1/datadog"
)

type DatadogClient struct {
	apiKey string
	appKey string
	client *dclient.APIClient
}

func NewDatadogClient(apiKey, appKey string) *DatadogClient {
	configuration := dclient.NewConfiguration()
	datadogCli := dclient.NewAPIClient(configuration)
	return &DatadogClient{
		apiKey: apiKey,
		appKey: appKey,
		client: datadogCli,
	}
}

func (c *DatadogClient) GetRequestCounts(ctx context.Context, monitoringRange int) (*RequestCountsResult, error) {
	ddCtx := context.WithValue(
		ctx,
		dclient.ContextAPIKeys,
		map[string]dclient.APIKey{
			"apiKeyAuth": {
				Key: c.apiKey,
			},
			"appKeyAuth": {
				Key: c.appKey,
			},
		},
	)
	to := time.Now().Unix()
	from := to - int64(monitoringRange)
	query := "sum:http_server_request_count{*} by {http.host}.as_count()"

	resp, _, err := c.client.MetricsApi.QueryMetrics(ddCtx).Query(query).From(from).To(to).Execute()
	if err != nil {
		return nil, err
	}
	requestCount := make(map[string]float64)
	total := float64(0)
	maxHost := ""
	maxCount := float64(0)
	for _, se := range *resp.Series {
		scope := se.GetScope()
		host := strings.TrimPrefix(scope, "http.host:")
		pl, ok := se.GetPointlistOk()
		count := float64(0)
		if ok {
			timestamp := float64(0)
			for _, point := range *pl {
				if point[0] > timestamp {
					count = point[1]
				}
			}
		}
		if count > maxCount {
			maxHost = host
			maxCount = count
		}
		requestCount[host] = count
		total += count
	}
	return &RequestCountsResult{
		TotalCounts: total,
		MaxHost:     maxHost,
		result:      requestCount,
	}, nil
}

type RequestCountsResult struct {
	TotalCounts float64
	MaxHost     string
	result      map[string]float64
}

func (r *RequestCountsResult) String() string {
	return fmt.Sprintf("TotalCounts: %v, MaxHost: %s, result: %v", r.TotalCounts, r.MaxHost, r.result)
}

func (r *RequestCountsResult) GetCounts(host string) float64 {
	return r.result[host]
}
