package kube

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	RestConfig    *rest.Config
	Clientset     *kubernetes.Clientset
	DynamicClient dynamic.Interface
	Mapper        meta.RESTMapper
}

func NewClient(kubeConfigPath string) (*Client, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kube rest config: %w", err)
	}

	return newClientFromRestConfig(restCfg)
}

func NewInClusterClient() (*Client, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("build in-cluster kube rest config: %w", err)
	}

	return newClientFromRestConfig(restCfg)
}

func newClientFromRestConfig(restCfg *rest.Config) (*Client, error) {
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create typed kube client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic kube client: %w", err)
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(clientset.Discovery()),
	)

	return &Client{
		RestConfig:    restCfg,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Mapper:        mapper,
	}, nil
}
