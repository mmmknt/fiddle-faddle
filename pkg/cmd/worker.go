package cmd

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"

	"github.com/mmmknt/fiddle-faddle/pkg/client"
)

type Options struct {
	BufferDestinationHost   string
	InternalDestinationHost string
}

type Worker struct {
	namespace string
	threashold int
	interval int
	istioCli *client.IstioClient
	kubeCli *client.KubernetesClient
	ddCli *client.DatadogClient
	externalDestinationHost string
	internalDestinationHost string
}

var (
	o = &Options{}
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
			return worker.work()
		},
	}

	cmd.Flags().StringVar(&o.BufferDestinationHost, "bufferHost", "", "buffer destination host")
	cmd.Flags().StringVar(&o.InternalDestinationHost, "internalHost", "", "internal destination host")
	cmd.MarkFlagRequired("bufferHost")
	cmd.MarkFlagRequired("internalHost")

	return cmd
}

func NewWorker(options *Options) (*Worker, error) {
	log.Printf("bufferHost: %s, internalHost: %s\n", options.BufferDestinationHost, options.InternalDestinationHost)

	namespace := "default"

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	istioCli, err := client.NewIstioClient(namespace, options.InternalDestinationHost, options.BufferDestinationHost, restConfig)
	if err != nil {
		log.Fatalf("Failed to create istio client: %s", err)
		return nil, err
	}

	kubeCli, err := client.NewKubernetesClient(namespace, restConfig)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
		return nil, err
	}

	ddCli := client.NewDatadogClient(os.Getenv("DD_CLIENT_API_KEY"), os.Getenv("DD_CLIENT_APP_KEY"))

	return &Worker{
		namespace: "default",
		threashold: 70,
		interval: 30,
		istioCli:                istioCli,
		kubeCli:                 kubeCli,
		ddCli:                   ddCli,
		externalDestinationHost: options.BufferDestinationHost,
		internalDestinationHost: options.InternalDestinationHost,
	}, nil
}

func (w *Worker) work() error {
	for {
		// operation interval
		time.Sleep(time.Duration(w.interval) * time.Second)

		source, err := w.monitor()
		if err != nil {
			continue
		}
		from, err := w.getRoutingRule()
		if err != nil {
			continue
		}

		to, err := w.calculate(source, from)
		if err != nil {
			log.Printf("Failed to calculate routing rule: %s\n", err)
			continue
		}

		if err = w.apply(to); err != nil {
			log.Printf("Failed to apply routing rule: %s\n", err)
			continue
		}
	}
	return nil
}

func (w *Worker) monitor() (*ruleSource, error) {
	log.Println("list HPA")
	hpalist, err := w.kubeCli.ListHPA(context.TODO())
	if err != nil {
		log.Printf("Failed to get HPA: %s", err)
		return nil, err
	}

	maxCurrentCPUUtilizationPercentage := int32(0)
	for i := range hpalist.Items {
		hpae := hpalist.Items[i]
		spec := hpae.Spec
		status := hpae.Status
		log.Printf("Index: %d, Scale target: %v, CurrentReplicas: %d, Current/Target CPUUtilizationPercentage: %d / %d\n",
			i, spec.ScaleTargetRef.Name, status.CurrentReplicas, *status.CurrentCPUUtilizationPercentage, *spec.TargetCPUUtilizationPercentage)
		if *status.CurrentCPUUtilizationPercentage >= maxCurrentCPUUtilizationPercentage {
			maxCurrentCPUUtilizationPercentage = *status.CurrentCPUUtilizationPercentage
		}
	}

	log.Println("get request count per host")
	requestCounts, err := w.ddCli.GetRequestCounts(context.TODO(), w.interval*2)
	if err != nil {
		log.Printf("Failed to get metrics: %s\n", err)
		return nil, err
	}
	return &ruleSource{
		currentValue: int(maxCurrentCPUUtilizationPercentage),
		requestCounts: requestCounts,
	}, nil
}

func (w *Worker) getRoutingRule() (*routingRule, error) {
	log.Println("list VirtualServices")
	vsList, err := w.istioCli.ListVirtualService(context.TODO())
	if err != nil {
		log.Printf("Failed to list vsList: %s", err)
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
		log.Printf("Index: %d Gnerated: %v, VirtualService Hosts: %+v\n", i, ag, vs.Spec.GetHosts())
		if ag == "true" {
			generated = true
			targetHost = vs.Spec.GetHosts()[0]
			version = vs.ObjectMeta.ResourceVersion
			for _, dest := range vs.Spec.GetHttp()[0].GetRoute() {
				dw := dest.GetWeight()
				dh := dest.Destination.Host
				log.Printf("destination host: %s\n", dh)
				if dh == w.internalDestinationHost {
					internalWeight = int(dw)
				} else if dh == w.externalDestinationHost {
					externalWeight = int(dw)
				}
			}
		}
	}

	return &routingRule{
		generated: generated,
		version: version,
		targetHost:     targetHost,
		internalWeight: internalWeight,
		externalWeight: externalWeight,
	}, nil
}

func (w *Worker) calculate(source *ruleSource, from *routingRule) (*routingRule, error) {
	targetHost := source.requestCounts.MaxHost
	targetRequestCount := source.requestCounts.GetCounts(targetHost)
	totalRequestCount := source.requestCounts.TotalCounts
	currentValue := source.currentValue

	// change state from current state
	internalWeight := 100
	externalWeight := 0
	log.Printf("maxCurrentCPUUtilization: %v, totalRequestCount: %v, targetRequestCount: %v, internalWeight: %v, bufferWeight: %v\n", currentValue, totalRequestCount, targetRequestCount, from.internalWeight, from.externalWeight)
	if currentValue > w.threashold {
		// decrease internal request count
		wantToDecreaseRequestCount := (currentValue - w.threashold) * int(totalRequestCount) / currentValue
		totalTargetHostRequest := int(targetRequestCount) * 100 / from.internalWeight
		internalWeight = int((targetRequestCount - float64(wantToDecreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
		externalWeight = 100 - internalWeight
		log.Printf("wantToDecreaseRequest: %v\n", wantToDecreaseRequestCount)
	} else if currentValue < 50 && from.externalWeight > 0 {
		// increase internal request count
		wantToIncreaseRequestCount := (60 - currentValue) * int(totalRequestCount) / currentValue
		totalTargetHostRequest := int(targetRequestCount) * 100 / from.internalWeight
		internalWeight = int((targetRequestCount + float64(wantToIncreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
		externalWeight = 100 - internalWeight
		log.Printf("wantToIncraseRequest: %v\n", wantToIncreaseRequestCount)
	}
	log.Printf("latest status. targetHost: %s, internalWeight: %v, bufferWeight: %v\n", targetHost, internalWeight, externalWeight)
	return &routingRule{
		generated:      from.generated,
		version:        from.version,
		targetHost:     targetHost,
		internalWeight: internalWeight,
		externalWeight: externalWeight,
	}, nil
}

func (w *Worker) apply(rule *routingRule) error {
	if rule.internalWeight >= 100 {
		if rule.generated {
			err := w.istioCli.DeleteVirtualService(context.TODO(), rule.targetHost)
			if err != nil {
				log.Printf("failed to delete VirtualService: %s", err)
				return err
			}
		}
	} else {
		log.Printf("need to update\n")
		if !rule.generated {
			err := w.istioCli.CreateVirtualService(context.TODO(), rule.targetHost, rule.internalWeight, rule.externalWeight)
			if err != nil {
				log.Printf("failed to create VirtualService: %s", err)
				return err
			}
		} else {
			err := w.istioCli.UpdateVirtualService(context.TODO(), rule.targetHost, rule.version, rule.internalWeight, rule.externalWeight)
			if err != nil {
				log.Printf("failed to update VirtualService: %s", err)
				return err
			}
		}
	}
	return nil
}

type ruleSource struct {
	currentValue int
	requestCounts *client.RequestCountsResult
}

type routingRule struct {
	generated bool
	version string
	targetHost string
	internalWeight int
	externalWeight int
}