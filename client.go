package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	dclient "github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	networkingv1alpha3 "istio.io/api/networking/v1alpha3"
	"istio.io/client-go/pkg/apis/networking/v1alpha3"
	versionedclient "istio.io/client-go/pkg/clientset/versioned"
	v1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	bufferDestinationHost = "external host"
	internalDestinationHost = "sorry"
	totalRequestCountKey = "total"
)

func main() {
	// prepare some clients
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	ic, err := versionedclient.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Failed to create istio client: %s", err)
	}

	kubeCS, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %s", err)
	}

	configuration := dclient.NewConfiguration()
	datadogCli := dclient.NewAPIClient(configuration)
	ddCtx := context.WithValue(
		context.Background(),
		dclient.ContextAPIKeys,
		map[string]dclient.APIKey{
			"apiKeyAuth": {
				Key: os.Getenv("DD_CLIENT_API_KEY"),
			},
			"appKeyAuth": {
				Key: os.Getenv("DD_CLIENT_APP_KEY"),
			},
		},
	)

	namespace := "default"
	for {
		// operation interval
		time.Sleep(30 * time.Second)

		// get current state
		maxCurrentCPUUtilizationPercentage := int32(0)

		log.Println("list HPA")
		hpalist, err := listHPA(kubeCS, namespace)
		if err != nil {
			log.Fatalf("Failed to get HPA: %s", err)
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
		vsList, err := listVirtualService(ic, namespace)
		if err != nil {
			log.Fatalf("Failed to list vsList: %s", err)
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
		requestCounts, err := getMetrics(ddCtx, datadogCli)
		if err != nil {
			log.Fatalf("Failed to get metrics: %s\n", err)
			continue
		}
		log.Printf("summation per host: %v\n", requestCounts)
		targetRequestCount := float64(0)
		totalRequestCount := float64(0)
		for key, value := range *requestCounts {
			if value > targetRequestCount && key != totalRequestCountKey {
				targetRequestCount = value
				if targetHost != key {
					log.Printf("current target is different to max request host. current: %s, max: %s\n", targetHost, key)
				}
				targetHost = key
			}
			if key == totalRequestCountKey {
				totalRequestCount = value
			}
		}

		// change state from current state
		log.Printf("maxCurrentCPUUtilization: %v, totalRequestCount: %v, targetRequestCount: %v, internalWeight: %v, bufferWeight: %v\n", maxCurrentCPUUtilizationPercentage, totalRequestCount, targetRequestCount, internalWeight, bufferWeight)
		if maxCurrentCPUUtilizationPercentage > 70 {
			// decrease internal request count
			wantToDecreaseRequestCount := (maxCurrentCPUUtilizationPercentage - 70) * int32(totalRequestCount)/maxCurrentCPUUtilizationPercentage
			totalTargetHostRequest := int(targetRequestCount) * 100 / internalWeight
			internalWeight = int((targetRequestCount - float64(wantToDecreaseRequestCount))/float64(totalTargetHostRequest) * 100)
			bufferWeight = 100 - internalWeight
			log.Printf("wantToDecreaseRequest: %v\n", wantToDecreaseRequestCount)
		} else if maxCurrentCPUUtilizationPercentage < 50 && bufferWeight > 0 {
			// increase internal request count
			wantToIncreaseRequestCount := (60 - maxCurrentCPUUtilizationPercentage) * int32(totalRequestCount)/maxCurrentCPUUtilizationPercentage
			totalTargetHostRequest := int(targetRequestCount) * 100 / internalWeight
			internalWeight = int((targetRequestCount + float64(wantToIncreaseRequestCount))/float64(totalTargetHostRequest) * 100)
			bufferWeight = 100 - internalWeight
			log.Printf("wantToIncraseRequest: %v\n", wantToIncreaseRequestCount)
		}
		log.Printf("latest status. targetHost: %s, internalWeight: %v, bufferWeight: %v\n", targetHost, internalWeight, bufferWeight)

		if internalWeight >= 100 {
			if vsGenerated {
				err := deleteVirtualService(ic, namespace, targetHost)
				if err != nil {
					log.Fatalf("failed to delete VirtualService: %s", err)
				}
			}
		} else {
			log.Printf("need to update\n")
			if !vsGenerated {
				err := createVirtualService(ic, namespace, targetHost, internalWeight, bufferWeight)
				if err != nil {
					log.Fatalf("failed to create VirtualService: %s", err)
				}
			} else {
				err := updateVirtualService(ic, namespace, targetHost, currentVSVersion, internalWeight, bufferWeight)
				if err != nil {
					log.Fatalf("failed to update VirtualService: %s", err)
				}
			}
		}
	}
}

func getMetrics(ctx context.Context, apiClient *dclient.APIClient) (*map[string]float64, error) {
	to := time.Now().Unix() // int64 | Start of the queried time period, seconds since the Unix epoch.
	from := to-120 // int64 | End of the queried time period, seconds since the Unix epoch.
	query := "http_server_request_count{*}by{http.host}" // string | Query string.

	resp, _, err := apiClient.MetricsApi.QueryMetrics(ctx).Query(query).From(from).To(to).Execute()
	if err != nil {
		return nil, err
	}
	requestCount := make(map[string]float64)
	total := float64(0)
	for _, se := range *resp.Series {
		scope := se.GetScope()
		host := strings.TrimPrefix(scope, "http.host:")
		pl, ok := se.GetPointlistOk()
		sum := float64(0)
		if ok {
			for _, point := range *pl {
				sum += point[1]
				total += point[1]
			}
		}
		requestCount[host] = sum
	}
	requestCount[totalRequestCountKey] = total
	return &requestCount, nil
}

func listHPA(kubeCS *kubernetes.Clientset, namespace string) (*v1.HorizontalPodAutoscalerList, error) {
	return kubeCS.AutoscalingV1().HorizontalPodAutoscalers(namespace).List(context.TODO(), metav1.ListOptions{})
}

func listVirtualService(ic *versionedclient.Clientset, namespace string) (*v1alpha3.VirtualServiceList, error) {
	return ic.NetworkingV1alpha3().VirtualServices(namespace).List(context.TODO(), metav1.ListOptions{})
}

func createVirtualService(ic *versionedclient.Clientset, namespace, host string, internalWeight, bufferWeight int) error {
		log.Println("create virtualservice")
		vs := getVirtualServiceModel(host, internalWeight, bufferWeight)

		create, err := ic.NetworkingV1alpha3().VirtualServices(namespace).Create(context.TODO(), vs, metav1.CreateOptions{})
		if err != nil {
			log.Fatalf("Failed to create virutalserivce in %s namespace: %s", namespace, err)
		}
		log.Printf("succeed to create virtualservice:%v\n", create)
		return err
}

func updateVirtualService(ic *versionedclient.Clientset, namespace, host, currentResourceVersion string, internalWeight, bufferWeight int) error {
	log.Println("update VirtualService")
	vs := getVirtualServiceModel(host, internalWeight, bufferWeight)
	vs.ObjectMeta.ResourceVersion = currentResourceVersion
	update, err := ic.NetworkingV1alpha3().VirtualServices(namespace).Update(context.TODO(), vs, metav1.UpdateOptions{})
	if err != nil {
		log.Fatalf("Failed to update virutalserivce in %s namespace: %s", namespace, err)
	}
	log.Printf("succeed to update virtualservice:%v\n", update)
	return err
}

func deleteVirtualService(ic *versionedclient.Clientset, namespace, host string) error {
	return ic.NetworkingV1alpha3().VirtualServices(namespace).Delete(context.TODO(), fmt.Sprintf("buffer-%s", host), metav1.DeleteOptions{})
}

func getVirtualServiceModel(host string, internalWeight, bufferWeight int) *v1alpha3.VirtualService {
	/*
		apiVersion: networking.istio.io/v1alpha3
		kind: VirtualService
		metadata:
		  name: sorry-buffer
		spec:
		  hosts:
		  # TODO
		  - target host
		  gateways:
		  # TODO
		  - target gateway
		  http:
		  - route:
		    - destination:
		  		# TODO
		        host: external host
		        port:
		          number: 443
		      # TODO change automatically
		      weight: 30
		      headers:
		        request:
		          set:
		      		# TODO change automatically
		            x-original-host: original host
		    - destination:
		  		# TODO
		        host: internal host
		        port:
		          number: 8080
		      # TODO change automatically
		      weight: 70
		      headers:
		        request:
		          set:
					# TODO change automatically
		            x-original-host: original host
		    rewrite:
	          # TODO change automatically
		      authority: original host
	*/
	return &v1alpha3.VirtualService{
		ObjectMeta: metav1.ObjectMeta{
			Name:                       fmt.Sprintf("buffer-%s", host),
			Labels: map[string]string{"auto-generated": "true"},
		},
		Spec:       networkingv1alpha3.VirtualService{
			Hosts:                []string{host},
			Gateways:             []string{"greeting-gateway"},
			Http:                 []*networkingv1alpha3.HTTPRoute{
				&networkingv1alpha3.HTTPRoute{
					Route:                []*networkingv1alpha3.HTTPRouteDestination{
						&networkingv1alpha3.HTTPRouteDestination{
							Destination:          &networkingv1alpha3.Destination{
								Host:                 bufferDestinationHost,
								Port:                 &networkingv1alpha3.PortSelector{
									Number:               443,
								},
							},
							Weight: int32(bufferWeight),
							Headers: &networkingv1alpha3.Headers{
								Request:              &networkingv1alpha3.Headers_HeaderOperations{
									Set:                  map[string]string{"x-original-host":host},
								},
							},
						},
						{
							Destination:          &networkingv1alpha3.Destination{
								Host:                 internalDestinationHost,
								Port:                 &networkingv1alpha3.PortSelector{
									Number:               8080,
								},
							},
							Weight: int32(internalWeight),
							Headers: &networkingv1alpha3.Headers{
								Request:              &networkingv1alpha3.Headers_HeaderOperations{
									Set:                  map[string]string{"x-original-host":host},
								},
							},
						},
					},
					Rewrite: &networkingv1alpha3.HTTPRewrite{
						Authority:            bufferDestinationHost,
					},
				},
			},
		},
	}
}
