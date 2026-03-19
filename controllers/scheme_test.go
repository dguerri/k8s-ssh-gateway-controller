package controllers

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestSetupScheme(t *testing.T) {
	scheme := SetupScheme()

	expectedSchemes := []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		gatewayv1.Install,
		gatewayv1alpha2.Install,
	}

	for _, add := range expectedSchemes {
		builder := runtime.NewSchemeBuilder(add)
		if err := builder.AddToScheme(runtime.NewScheme()); err != nil {
			t.Errorf("failed to add scheme: %v", err)
		}
	}

	if scheme == nil {
		t.Error("SetupScheme() returned nil")
	}
}
