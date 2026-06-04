package controllers

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestExtractTLSRouteDetails(t *testing.T) {
	namespace := "test-namespace"
	routeName := "test-route"
	gwName := "test-gateway"
	listenerName := "tls-listener"
	backendHost := "test-service"
	backendPort := 8443

	valid := &gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName, Namespace: namespace},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	}

	rd, err := extractTLSRouteDetails(valid)
	if err != nil {
		t.Fatal(err)
	}
	if rd.listenerName != listenerName || rd.backendPort != backendPort {
		t.Fatalf("unexpected details: %+v", rd)
	}
	if rd.routeName != routeName || rd.routeNamespace != namespace {
		t.Fatalf("unexpected route identity: %+v", rd)
	}
	if rd.gwName != gwName || rd.gwNamespace != namespace {
		t.Fatalf("unexpected gateway identity: %+v", rd)
	}
	if rd.backendHost != getSvcHostname(backendHost, namespace) {
		t.Fatalf("unexpected backend host: %s", rd.backendHost)
	}
}

func TestExtractTLSRouteDetails_NoParentRefs(t *testing.T) {
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec:       gatewayv1alpha2.TLSRouteSpec{},
	})
	if err == nil {
		t.Fatal("expected error for no parent refs")
	}
	if rd != nil {
		t.Fatal("expected nil details on error")
	}
	if err.Error() != "TLSRoute ns/r must have at least one ParentRef" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
}

func TestExtractTLSRouteDetails_NoRules(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error and nil details for no rules")
	}
}

func TestExtractTLSRouteDetails_MultipleParentRefs(t *testing.T) {
	// Exercises the "more than one ParentRef" debug log path — should still succeed
	// using the first ref.
	gwName := "gw"
	listenerName := "tls"
	backendHost := "svc"
	backendPort := 443
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:        gatewayv1.ObjectName(gwName),
						SectionName: (*gatewayv1.SectionName)(&listenerName),
					},
					{
						Name:        gatewayv1.ObjectName("other-gw"),
						SectionName: (*gatewayv1.SectionName)(&listenerName),
					},
				},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rd.gwName != gwName {
		t.Fatalf("expected gwName %s, got %s", gwName, rd.gwName)
	}
}

func TestExtractTLSRouteDetails_EmptyParentName(t *testing.T) {
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "",
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for empty parent name")
	}
}

func TestExtractTLSRouteDetails_ExplicitParentNamespace(t *testing.T) {
	// Exercises the parent.Namespace != nil branch.
	gwName := "gw"
	listenerName := "tls"
	gwNamespace := "gw-ns"
	backendHost := "svc"
	backendPort := 443
	gwNS := gatewayv1.Namespace(gwNamespace)
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					Namespace:   &gwNS,
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rd.gwNamespace != gwNamespace {
		t.Fatalf("expected gwNamespace %s, got %s", gwNamespace, rd.gwNamespace)
	}
}

func TestExtractTLSRouteDetails_NilSectionName(t *testing.T) {
	gwName := "gw"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: gatewayv1.ObjectName(gwName),
					// SectionName intentionally nil
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for nil sectionName")
	}
}

func TestExtractTLSRouteDetails_NoBackendRefs(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for no backend refs")
	}
}

func TestExtractTLSRouteDetails_EmptyBackendName(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "",
					},
				}},
			}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for empty backend name")
	}
}

func TestExtractTLSRouteDetails_NilBackendPort(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "svc",
						// Port intentionally nil
					},
				}},
			}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for nil backend port")
	}
}

func TestExtractTLSRouteDetails_ExplicitBackendNamespace(t *testing.T) {
	// Exercises the k8Svc.Namespace != nil branch.
	gwName := "gw"
	listenerName := "tls"
	backendHost := "svc"
	backendPort := 443
	backendNS := gatewayv1.Namespace("backend-ns")
	rd, err := extractTLSRouteDetails(&gatewayv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1alpha2.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1alpha2.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name:      gatewayv1.ObjectName(backendHost),
						Namespace: &backendNS,
						Port:      (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHost := getSvcHostname(backendHost, "backend-ns")
	if rd.backendHost != expectedHost {
		t.Fatalf("expected backendHost %s, got %s", expectedHost, rd.backendHost)
	}
}

func TestGetTLSRouteFinalizer(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "pico.sh/test-controller")
	got := getTLSRouteFinalizer()
	want := "tlsroute.gateway.networking.k8s.io/finalizer-pico.sh-test-controller"
	if got != want {
		t.Fatalf("unexpected finalizer: %s (want %s)", got, want)
	}
}
