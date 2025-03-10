package controllers

import (
	"context"
	"fmt"

	v1 "github.com/infinispan/infinispan-operator/api/v1"
	consts "github.com/infinispan/infinispan-operator/controllers/constants"
	"github.com/infinispan/infinispan-operator/pkg/http/curl"
	"github.com/infinispan/infinispan-operator/pkg/infinispan/client"
	"github.com/infinispan/infinispan-operator/pkg/infinispan/client/api"
	users "github.com/infinispan/infinispan-operator/pkg/infinispan/security"
	kube "github.com/infinispan/infinispan-operator/pkg/kubernetes"
)

// NewInfinispan returns a new api.Infinispan client using the first pod in the cluster's StatefulSet
func NewInfinispan(ctx context.Context, i *v1.Infinispan, kubernetes *kube.Kubernetes) (api.Infinispan, error) {
	podList, err := PodsCreatedBy(i.Namespace, kubernetes, ctx, i.GetStatefulSetName())
	if err != nil {
		return nil, err
	}
	return NewInfinispanForPod(ctx, podList.Items[0].Name, i, kubernetes)
}

// NewInfinispanForPod retrieves credential information to initialise a curl.Client and uses this to return a api.Infinispan implementation
func NewInfinispanForPod(ctx context.Context, podName string, i *v1.Infinispan, kubernetes *kube.Kubernetes) (api.Infinispan, error) {
	curl, err := NewCurlClient(ctx, podName, i, kubernetes)
	if err != nil {
		return nil, fmt.Errorf("unable to create Infinispan client: %w", err)
	}
	return client.New(curl), nil
}

// NewCurlClient return a new curl.Client using the admin credentials associated with the v1.Infinispan instance
func NewCurlClient(ctx context.Context, podName string, i *v1.Infinispan, kubernetes *kube.Kubernetes) (*curl.Client, error) {
	pass, err := users.AdminPassword(i.GetAdminSecretName(), i.Namespace, kubernetes, ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve operator admin identities when creating Curl client: %w", err)
	}
	curlClient := curl.New(curl.Config{
		Credentials: &curl.Credentials{
			Username: consts.DefaultOperatorUser,
			Password: pass,
		},
		Container: InfinispanContainer,
		Podname:   podName,
		Namespace: i.Namespace,
		Protocol:  "http",
		Port:      consts.InfinispanAdminPort,
	}, kubernetes)
	return curlClient, nil
}

// InfinispanForPod return a api.Infinispan based upon a clone of the provided curl.Client that uses the provided podname
// This method should be preferred over NewInfinispanForPod when a curl.Client already exists in order to prevent duplicate
// lookups of the admin credentials
func InfinispanForPod(podName string, c *curl.Client) api.Infinispan {
	cloneConfig := c.Config
	cloneConfig.Podname = podName
	curl := curl.New(cloneConfig, c.Kubernetes)
	return client.New(curl)
}
