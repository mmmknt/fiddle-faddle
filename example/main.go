package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"go.opentelemetry.io/contrib/exporters/metric/datadog"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/kv"
	"go.opentelemetry.io/otel/api/metric"
	"go.opentelemetry.io/otel/sdk/metric/aggregator/ddsketch"
	"go.opentelemetry.io/otel/sdk/metric/controller/push"
	"go.opentelemetry.io/otel/sdk/metric/selector/simple"
)

func main() {

	statsdHostIP := os.Getenv("DD_AGENT_HOST")
	if statsdHostIP != "" {
		do := datadog.Options{
			StatsAddr:     fmt.Sprintf("%s:8125", statsdHostIP),
			Tags:          []string{"namespace:test"},
			StatsDOptions: []statsd.Option{statsd.WithoutTelemetry()},
		}
		exporter, err := datadog.NewExporter(do)
		if err != nil {
			panic(err)
		}
		selector := simple.NewWithSketchDistribution(ddsketch.NewDefaultConfig())
		pusher := push.New(selector, exporter, push.WithPeriod(time.Second*10))
		global.SetMeterProvider(pusher.Provider())
		defer pusher.Stop()
		go func() {
			pusher.Start()
		}()
	}

	gmHandler := func(w http.ResponseWriter, r *http.Request) {
		ep := os.Getenv("EXECUTION_PLATFORM")
		host := r.Header.Get("x-original-host")
		if host == "" {
			host = r.Host
		}
		wg := &sync.WaitGroup{}
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				for i := 0; i < 1000; i++ {
					rand.Int(rand.Reader, big.NewInt(100))
				}
				wg.Done()
			}()
		}
		wg.Wait()
		io.WriteString(w, fmt.Sprintf("Good Morning from %s in %s\n", host, ep))
	}
	http.Handle("/greeting/morning", collectorFunc(http.HandlerFunc(gmHandler)))

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func collectorFunc(next http.Handler) http.Handler {
	requestCount := "http.server.request_count"
	counter := make(map[string]metric.Int64Counter)
	meter := global.Meter("mmmknt.dev/http")
	requestCounter, err := meter.NewInt64Counter(requestCount)
	if err != nil {
		global.Handle(err)
	}
	counter[requestCount] = requestCounter
	executionPlatform := os.Getenv("EXECUTION_PLATFORM")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("Executing statsd")
		host := r.Header.Get("x-original-host")
		if host == "" {
			host = r.Host
		}
		counter[requestCount].Add(context.TODO(), int64(1), []kv.KeyValue{kv.String("http.host", host), kv.String("http.platform", executionPlatform)}...)
		next.ServeHTTP(w, r)
	})

}
