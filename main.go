package main

import (
	"context"
	"log"
	"os"
	"time"

	"k8s.io/client-go/rest"

	"github.com/mmmknt/fiddle-faddle/pkg/client"
)

const (
	bufferDestinationHost   = "external host"
	internalDestinationHost = "sorry"
)

func main() {
	namespace := "default"
	// prepare some clients
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	istioCli, err := client.NewIstioClient(namespace, "internal", "external", restConfig)
	if err != nil {
		log.Fatalf("Failed to create istio client: %s", err)
	}

	kubeCli, err := client.NewKubernetesClient(namespace, restConfig)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	ddCli := client.NewDatadogClient(os.Getenv("DD_CLIENT_API_KEY"), os.Getenv("DD_CLIENT_APP_KEY"))

	for {
		// operation interval
		time.Sleep(30 * time.Second)

		// get current state
		maxCurrentCPUUtilizationPercentage := int32(0)

		log.Println("list HPA")
		hpalist, err := kubeCli.ListHPA(context.TODO())
		if err != nil {
			log.Printf("Failed to get HPA: %s", err)
			continue
		}
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

		log.Println("list VirtualServices")
		vsList, err := istioCli.ListVirtualService(context.TODO())
		if err != nil {
			log.Printf("Failed to list vsList: %s", err)
			continue
		}
		internalWeight := 100
		bufferWeight := 0
		targetHost := ""
		vsGenerated := false
		currentVSVersion := ""
		for i := range vsList.Items {
			vs := vsList.Items[i]
			generated := vs.GetLabels()["auto-generated"]
			log.Printf("Index: %d Gnerated: %v, VirtualService Hosts: %+v\n", i, generated, vs.Spec.GetHosts())
			if generated == "true" {
				vsGenerated = true
				targetHost = vs.Spec.GetHosts()[0]
				currentVSVersion = vs.ObjectMeta.ResourceVersion
				for _, dest := range vs.Spec.GetHttp()[0].GetRoute() {
					dw := dest.GetWeight()
					dh := dest.Destination.Host
					log.Printf("destination host: %s\n", dh)
					if dh == internalDestinationHost {
						internalWeight = int(dw)
					} else if dh == bufferDestinationHost {
						bufferWeight = int(dw)
					}
				}
			}
		}

		log.Println("get request count per host")
		requestCounts, err := ddCli.GetRequestCounts(context.TODO(), 120)
		if err != nil {
			log.Printf("Failed to get metrics: %s\n", err)
			continue
		}
		log.Printf("summation per host: %v\n", requestCounts)
		targetRequestCount := requestCounts.GetCounts(requestCounts.MaxHost)
		totalRequestCount := requestCounts.TotalCounts

		// change state from current state
		log.Printf("maxCurrentCPUUtilization: %v, totalRequestCount: %v, targetRequestCount: %v, internalWeight: %v, bufferWeight: %v\n", maxCurrentCPUUtilizationPercentage, totalRequestCount, targetRequestCount, internalWeight, bufferWeight)
		if maxCurrentCPUUtilizationPercentage > 70 {
			// decrease internal request count
			wantToDecreaseRequestCount := (maxCurrentCPUUtilizationPercentage - 70) * int32(totalRequestCount) / maxCurrentCPUUtilizationPercentage
			totalTargetHostRequest := int(targetRequestCount) * 100 / internalWeight
			internalWeight = int((targetRequestCount - float64(wantToDecreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
			bufferWeight = 100 - internalWeight
			log.Printf("wantToDecreaseRequest: %v\n", wantToDecreaseRequestCount)
		} else if maxCurrentCPUUtilizationPercentage < 50 && bufferWeight > 0 {
			// increase internal request count
			wantToIncreaseRequestCount := (60 - maxCurrentCPUUtilizationPercentage) * int32(totalRequestCount) / maxCurrentCPUUtilizationPercentage
			totalTargetHostRequest := int(targetRequestCount) * 100 / internalWeight
			internalWeight = int((targetRequestCount + float64(wantToIncreaseRequestCount)) / float64(totalTargetHostRequest) * 100)
			bufferWeight = 100 - internalWeight
			log.Printf("wantToIncraseRequest: %v\n", wantToIncreaseRequestCount)
		}
		log.Printf("latest status. targetHost: %s, internalWeight: %v, bufferWeight: %v\n", targetHost, internalWeight, bufferWeight)

		if internalWeight >= 100 {
			if vsGenerated {
				err := istioCli.DeleteVirtualService(context.TODO(), targetHost)
				if err != nil {
					log.Fatalf("failed to delete VirtualService: %s", err)
				}
			}
		} else {
			log.Printf("need to update\n")
			if !vsGenerated {
				err := istioCli.CreateVirtualService(context.TODO(), targetHost, internalWeight, bufferWeight)
				if err != nil {
					log.Fatalf("failed to create VirtualService: %s", err)
				}
			} else {
				err := istioCli.UpdateVirtualService(context.TODO(), targetHost, currentVSVersion, internalWeight, bufferWeight)
				if err != nil {
					log.Fatalf("failed to update VirtualService: %s", err)
				}
			}
		}
	}
}
