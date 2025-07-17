package utils

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// GetServiceNodePort retrieves the NodePort for a given service and port number in a namespace.
func GetServiceNodePort(kubeConfig string, namespace, serviceName string, portNumber int32) (int32, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return 0, fmt.Errorf("failed to build k8s config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return 0, fmt.Errorf("failed to create k8s clientset: %w", err)
	}

	service, err := clientset.CoreV1().Services(namespace).Get(context.Background(), serviceName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get service: %w", err)
	}
	for _, port := range service.Spec.Ports {
		if portNumber != 0 && port.Port == portNumber {
			if port.NodePort != 0 {
				return port.NodePort, nil
			}
		}
	}
	return 0, fmt.Errorf("nodePort not found for service %s", serviceName)
}
