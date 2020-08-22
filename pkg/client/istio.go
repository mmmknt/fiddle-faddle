package client

import (
	"context"
	"fmt"
	"log"

	networkingv1alpha3 "istio.io/api/networking/v1alpha3"
	"istio.io/client-go/pkg/apis/networking/v1alpha3"
	"istio.io/client-go/pkg/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type IstioClient struct {
	namespace           string
	internalDestination string
	externalDestination string
	clientSet           *versioned.Clientset
}

func NewIstioClient(namespace, internalDestinationHost, externalDestinationHost string, config *rest.Config) (*IstioClient, error) {
	cs, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &IstioClient{
		clientSet:           cs,
		namespace:           namespace,
		internalDestination: internalDestinationHost,
		externalDestination: externalDestinationHost,
	}, nil
}

func (c *IstioClient) ListVirtualService(ctx context.Context) (*v1alpha3.VirtualServiceList, error) {
	return c.clientSet.NetworkingV1alpha3().VirtualServices(c.namespace).List(ctx, metav1.ListOptions{})
}

func (c *IstioClient) DeleteVirtualService(ctx context.Context, host string) error {
	return c.clientSet.NetworkingV1alpha3().VirtualServices(c.namespace).Delete(ctx, fmt.Sprintf("buffer-%s", host), metav1.DeleteOptions{})
}

func (c *IstioClient) CreateVirtualService(ctx context.Context, host string, internalWeight, bufferWeight int) error {
	log.Println("create virtualservice")
	vs := c.getVirtualServiceModel(host, internalWeight, bufferWeight)

	create, err := c.clientSet.NetworkingV1alpha3().VirtualServices(c.namespace).Create(ctx, vs, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Failed to create virutalserivce: %v\n", err)
	}
	log.Printf("succeed to create virtualservice:%v\n", create)
	return err
}

func (c *IstioClient) UpdateVirtualService(ctx context.Context, host, currentResourceVersion string, internalWeight, bufferWeight int) error {
	log.Println("update VirtualService")
	vs := c.getVirtualServiceModel(host, internalWeight, bufferWeight)
	vs.ObjectMeta.ResourceVersion = currentResourceVersion
	update, err := c.clientSet.NetworkingV1alpha3().VirtualServices(c.namespace).Update(ctx, vs, metav1.UpdateOptions{})
	if err != nil {
		log.Fatalf("Failed to update virutalserivce: %v\n", err)
	}
	log.Printf("succeed to update virtualservice:%v\n", update)
	return err
}

func (c *IstioClient) getVirtualServiceModel(host string, internalWeight, bufferWeight int) *v1alpha3.VirtualService {
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
			Name:   fmt.Sprintf("buffer-%s", host),
			Labels: map[string]string{"auto-generated": "true"},
		},
		Spec: networkingv1alpha3.VirtualService{
			Hosts:    []string{host},
			Gateways: []string{"greeting-gateway"},
			Http: []*networkingv1alpha3.HTTPRoute{
				&networkingv1alpha3.HTTPRoute{
					Route: []*networkingv1alpha3.HTTPRouteDestination{
						&networkingv1alpha3.HTTPRouteDestination{
							Destination: &networkingv1alpha3.Destination{
								Host: c.externalDestination,
								Port: &networkingv1alpha3.PortSelector{
									Number: 443,
								},
							},
							Weight: int32(bufferWeight),
							Headers: &networkingv1alpha3.Headers{
								Request: &networkingv1alpha3.Headers_HeaderOperations{
									Set: map[string]string{"x-original-host": host},
								},
							},
						},
						{
							Destination: &networkingv1alpha3.Destination{
								Host: c.internalDestination,
								Port: &networkingv1alpha3.PortSelector{
									Number: 8080,
								},
							},
							Weight: int32(internalWeight),
							Headers: &networkingv1alpha3.Headers{
								Request: &networkingv1alpha3.Headers_HeaderOperations{
									Set: map[string]string{"x-original-host": host},
								},
							},
						},
					},
					Rewrite: &networkingv1alpha3.HTTPRewrite{
						Authority: c.externalDestination,
					},
				},
			},
		},
	}
}
