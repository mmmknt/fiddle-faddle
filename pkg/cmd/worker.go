package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/rest"

	"github.com/mmmknt/fiddle-faddle/pkg/client"
)

type Options struct {
	BufferDestinationHost   string
	InternalDestinationHost string
	LogLevel                string
}

type Worker struct {
	threshold               int
	interval                int
	istioCli                *client.IstioClient
	kubeCli                 *client.KubernetesClient
	ddCli                   *client.DatadogClient
	externalDestinationHost string
	internalDestinationHost string
	logger                  *zap.Logger
}

var (
	o = &Options{
		LogLevel: "INFO",
	}
)

func NewWorkerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "A worker starter",
		RunE: func(cmd *cobra.Command, args []string) error {
			worker, err := NewWorker(o)
			if err != nil {
				return nil
			}
			defer func() {
				worker.logger.Sync()
			}()
			return worker.work()
		},
	}

	cmd.Flags().StringVar(&o.BufferDestinationHost, "bufferHost", "", "buffer destination host")
	cmd.Flags().StringVar(&o.InternalDestinationHost, "internalHost", "", "internal destination host")
	cmd.Flags().StringVar(&o.LogLevel, "logLevel", o.LogLevel, "log level")
	cmd.MarkFlagRequired("bufferHost")
	cmd.MarkFlagRequired("internalHost")

	return cmd
}

func NewWorker(options *Options) (*Worker, error) {
	namespace := "default"

	logConfig := zap.NewProductionConfig()
	switch options.LogLevel {
	case "INFO":
		// nop
	case "DEBUG":
		logConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	default:
		return nil, errors.New(fmt.Sprintf("invalid LogLevel: %v", options.LogLevel))
	}
	logger, err := logConfig.Build()
	if err != nil {
		log.Printf("can't initialize zap logger: %v\n", err)
		return nil, err
	}
	logger = logger.Named("worker")

	logger.Info("initialize worker", zap.Any("options", options))
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("failed to create cluster config", zap.Error(err))
		return nil, err
	}

	istioCli, err := client.NewIstioClient(namespace, options.InternalDestinationHost, options.BufferDestinationHost, restConfig)
	if err != nil {
		logger.Error("failed to create Istio client", zap.Error(err))
		return nil, err
	}

	kubeCli, err := client.NewKubernetesClient(namespace, restConfig)
	if err != nil {
		logger.Error("failed to create Kubernetes client", zap.Error(err))
		return nil, err
	}

	ddCli := client.NewDatadogClient(os.Getenv("DD_CLIENT_API_KEY"), os.Getenv("DD_CLIENT_APP_KEY"))

	return &Worker{
		threshold:               70,
		interval:                30,
		istioCli:                istioCli,
		kubeCli:                 kubeCli,
		ddCli:                   ddCli,
		externalDestinationHost: options.BufferDestinationHost,
		internalDestinationHost: options.InternalDestinationHost,
		logger:                  logger,
	}, nil
}

func (w *Worker) work() error {
	logger := w.logger
	for {
		// operation interval
		time.Sleep(time.Duration(w.interval) * time.Second)
		logger.Info("working...")

		source, err := w.monitor()
		if err != nil {
			logger.Error("failed to monitor metrics", zap.Error(err))
			continue
		}
		from, err := w.getRoutingRule()
		if err != nil {
			logger.Error("failed to get current routing rule", zap.Error(err))
			continue
		}

		to, err := w.calculate(source, from)
		if err != nil {
			logger.Error("failed to calculate routing rule",
				zap.Error(err), zap.Any("source", source), zap.Any("from", from))
			continue
		}
		if to.equal(from) {
			logger.Debug("routing rules are not changed",
				zap.Any("from", from), zap.Any("to", to))
			continue
		}

		if err = w.apply(to); err != nil {
			logger.Error("failed to apply routing rule", zap.Error(err))
			continue
		}
	}
	return nil
}

func (w *Worker) monitor() (*ruleSource, error) {
	logger := w.logger

	hpalist, err := w.kubeCli.ListHPA(context.TODO())
	if err != nil {
		logger.Error("failed to list HPA", zap.Error(err))
		return nil, err
	}

	maxCurrentCPUUtilizationPercentage := int32(0)
	for i := range hpalist.Items {
		hpae := hpalist.Items[i]
		spec := hpae.Spec
		status := hpae.Status
		logger.Debug("HPA item status", zap.Int("index", i), zap.String("scale target", spec.ScaleTargetRef.Name),
			zap.Int32("current replicas", status.CurrentReplicas),
			zap.Int32("current cpu utilization percentage", *status.CurrentCPUUtilizationPercentage),
			zap.Int32("target cpu utilization percentage", *spec.TargetCPUUtilizationPercentage))
		if *status.CurrentCPUUtilizationPercentage >= maxCurrentCPUUtilizationPercentage {
			maxCurrentCPUUtilizationPercentage = *status.CurrentCPUUtilizationPercentage
		}
	}

	requestCounts, err := w.ddCli.GetRequestCounts(context.TODO(), w.interval*2)
	if err != nil {
		logger.Error("failed to get request counts", zap.Error(err))
		return nil, err
	}
	return &ruleSource{
		currentValue:  int(maxCurrentCPUUtilizationPercentage),
		requestCounts: requestCounts,
	}, nil
}

func (w *Worker) getRoutingRule() (*routingRule, error) {
	logger := w.logger
	vsList, err := w.istioCli.ListVirtualService(context.TODO())
	if err != nil {
		logger.Error("failed to list VirtualService", zap.Error(err))
		return nil, err
	}

	internalWeight := 100
	externalWeight := 0
	targetHost := ""
	generated := false
	version := ""
	for i := range vsList.Items {
		vs := vsList.Items[i]
		ag := vs.GetLabels()["auto-generated"]
		logger.Debug("VirtualService item status",
			zap.Int("index", i), zap.String("auto-generated", ag), zap.Any("hosts", vs.Spec.GetHosts()))
		if ag == "true" {
			generated = true
			targetHost = vs.Spec.GetHosts()[0]
			version = vs.ObjectMeta.ResourceVersion
			for _, dest := range vs.Spec.GetHttp()[0].GetRoute() {
				dw := dest.GetWeight()
				dh := dest.Destination.Host
				if dh == w.internalDestinationHost {
					internalWeight = int(dw)
				} else if dh == w.externalDestinationHost {
					externalWeight = int(dw)
				}
			}
		}
	}

	return &routingRule{
		generated:      generated,
		version:        version,
		targetHost:     targetHost,
		internalWeight: internalWeight,
		externalWeight: externalWeight,
	}, nil
}

func (w *Worker) calculate(source *ruleSource, from *routingRule) (*routingRule, error) {
	logger := w.logger.With(zap.Any("rule source", source)).With(zap.Any("current routing rule", from))
	logger.Debug("start to calculate")

	targetHost := source.requestCounts.MaxHost
	targetRequestCount := source.requestCounts.GetCounts(targetHost)
	totalRequestCount := source.requestCounts.TotalCounts
	currentValue := source.currentValue

	// change state from current state
	internalWeight := 100
	externalWeight := 0
	if currentValue > w.threshold {
		// decrease internal request count
		wantToDecreaseRequestCount := (currentValue - w.threshold) * int(totalRequestCount) / currentValue
		totalTargetHostRequest := int(targetRequestCount) * 100 / from.internalWeight
		internalWeight = int((targetRequestCount - float64(wantToDecreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
		externalWeight = 100 - internalWeight
	} else if currentValue < 50 && from.externalWeight > 0 {
		// increase internal request count
		wantToIncreaseRequestCount := (60 - currentValue) * int(totalRequestCount) / currentValue
		totalTargetHostRequest := int(targetRequestCount) * 100 / from.internalWeight
		internalWeight = int((targetRequestCount + float64(wantToIncreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
		externalWeight = 100 - internalWeight
	}
	logger.Debug("finish to calculate", zap.String("target host", targetHost),
		zap.Int("internal weight", internalWeight), zap.Int("external weight", externalWeight))
	return &routingRule{
		generated:      from.generated,
		version:        from.version,
		targetHost:     targetHost,
		internalWeight: internalWeight,
		externalWeight: externalWeight,
	}, nil
}

func (w *Worker) apply(rule *routingRule) error {
	logger := w.logger.With(zap.Any("rule", rule))
	logger.Debug("start to apply")
	if rule.internalWeight >= 100 {
		if rule.generated {
			err := w.istioCli.DeleteVirtualService(context.TODO(), rule.targetHost)
			if err != nil {
				logger.Error("failed to delete VirtualService", zap.Error(err))
				return err
			}
		}
	} else {
		if !rule.generated {
			err := w.istioCli.CreateVirtualService(context.TODO(), rule.targetHost, rule.internalWeight, rule.externalWeight)
			if err != nil {
				logger.Error("failed to create VirtualService", zap.Error(err))
				return err
			}
		} else {
			err := w.istioCli.UpdateVirtualService(context.TODO(), rule.targetHost, rule.version, rule.internalWeight, rule.externalWeight)
			if err != nil {
				logger.Error("failed to update VirtualService", zap.Error(err))
				return err
			}
		}
	}
	return nil
}

type ruleSource struct {
	currentValue  int
	requestCounts *client.RequestCountsResult
}

type routingRule struct {
	generated      bool
	version        string
	targetHost     string
	internalWeight int
	externalWeight int
}

func (r *routingRule) equal(rule *routingRule) bool {
	return r.targetHost == rule.targetHost &&
		r.internalWeight == rule.internalWeight &&
		r.externalWeight == rule.externalWeight
}
