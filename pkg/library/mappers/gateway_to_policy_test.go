//go:build unit

package mappers

import (
	"context"
	"testing"

	"gotest.tools/assert"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	kuadrantv1beta2 "github.com/kuadrant/kuadrant-operator/api/v1beta2"
	"github.com/kuadrant/kuadrant-operator/pkg/log"
)

func TestNewGatewayToPolicyEventMapper(t *testing.T) {
	testScheme := runtime.NewScheme()

	err := appsv1.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	err = gatewayapiv1.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	err = kuadrantv1beta2.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	clientBuilder := func(objs []runtime.Object) client.Client {
		return fake.NewClientBuilder().
			WithScheme(testScheme).
			WithRuntimeObjects(objs...).
			Build()
	}

	t.Run("not gateway related event", func(subT *testing.T) {
		objs := []runtime.Object{}
		cl := clientBuilder(objs)
		em := NewGatewayToPolicyEventMapper(kuadrantv1beta2.NewRateLimitPolicyType(), WithClient(cl), WithLogger(log.NewLogger()))
		requests := em.Map(ctx, &gatewayapiv1.HTTPRoute{})
		assert.DeepEqual(subT, []reconcile.Request{}, requests)
	})

	t.Run("gateway related event - no policies - no requests", func(subT *testing.T) {
		objs := []runtime.Object{}
		cl := clientBuilder(objs)
		em := NewGatewayToPolicyEventMapper(kuadrantv1beta2.NewRateLimitPolicyType(), WithClient(cl), WithLogger(log.NewLogger()))
		requests := em.Map(ctx, &gatewayapiv1.Gateway{})
		assert.DeepEqual(subT, []reconcile.Request{}, requests)
	})

	t.Run("gateway related event - requests", func(subT *testing.T) {
		gw := gatewayFactory("ns-a", "gw-1")
		route := routeFactory("ns-a", "route-1", gatewayapiv1.ParentReference{Name: "gw-1"})
		p1 := policyFactory("ns-a", "p-1", gatewayapiv1alpha2.PolicyTargetReference{
			Group: gatewayapiv1.GroupName,
			Kind:  "HTTPRoute",
			Name:  gatewayapiv1.ObjectName("route-1"),
		})
		objs := []runtime.Object{gw, route, p1}
		cl := clientBuilder(objs)
		em := NewGatewayToPolicyEventMapper(kuadrantv1beta2.NewRateLimitPolicyType(), WithClient(cl), WithLogger(log.NewLogger()))
		requests := em.Map(ctx, gw)
		expected := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "p-1"}}}
		assert.DeepEqual(subT, expected, requests)
	})
}
