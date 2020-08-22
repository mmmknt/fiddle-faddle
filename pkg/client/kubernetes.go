package client

import (
	"context"

	v1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type KubernetesClient struct {
	namespace string
	clientSet *kubernetes.Clientset
}

func NewKubernetesClient(namespace string, config *rest.Config) (*KubernetesClient, error) {
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &KubernetesClient{
		namespace: namespace,
		clientSet: cs,
	}, nil
}

func (c *KubernetesClient) ListHPA(ctx context.Context) (*v1.HorizontalPodAutoscalerList, error) {
	return c.clientSet.AutoscalingV1().HorizontalPodAutoscalers(c.namespace).List(ctx, metav1.ListOptions{})
}
