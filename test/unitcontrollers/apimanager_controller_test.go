package test

import (
	"context"
	"testing"

	appsv1alpha1 "github.com/3scale/3scale-operator/apis/apps/v1alpha1"
	"github.com/3scale/3scale-operator/pkg/reconcilers"

	appscontrollers "github.com/3scale/3scale-operator/controllers/apps"
	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	grafanav1alpha1 "github.com/integr8ly/grafana-operator/v3/pkg/apis/integreatly/v1alpha1"
	appsv1 "github.com/openshift/api/apps/v1"
	imagev1 "github.com/openshift/api/image/v1"
	routev1 "github.com/openshift/api/route/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestAPIManagerControllerCreate(t *testing.T) {
	var (
		name           = "example-apimanager"
		namespace      = "operator-unittest"
		wildcardDomain = "test.3scale.net"
	)

	ctx := context.TODO()

	apimanager := &appsv1alpha1.APIManager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1alpha1.APIManagerSpec{
			APIManagerCommonSpec: appsv1alpha1.APIManagerCommonSpec{
				WildcardDomain: wildcardDomain,
			},
		},
	}

	// Objects to track in the fake client.
	objs := []runtime.Object{apimanager}

	// Register operator types with the runtime scheme.
	s := scheme.Scheme
	s.AddKnownTypes(appsv1alpha1.GroupVersion, apimanager)
	err := appsv1.AddToScheme(s)
	if err != nil {
		t.Fatalf("Unable to add Apps scheme: (%v)", err)
	}
	err = imagev1.AddToScheme(s)
	if err != nil {
		t.Fatalf("Unable to add Image scheme: (%v)", err)
	}
	err = routev1.AddToScheme(s)
	if err != nil {
		t.Fatalf("Unable to add Route scheme: (%v)", err)
	}
	if err := monitoringv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := grafanav1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	// Create a fake client to mock API calls.
	cl := fake.NewFakeClient(objs...)
	clientAPIReader := fake.NewFakeClient(objs...)
	clientset := fakeclientset.NewSimpleClientset()
	recorder := record.NewFakeRecorder(10000)

	baseReconciler := reconcilers.NewBaseReconciler(ctx, cl, s, clientAPIReader, ctrl.Log.WithName("controllers").WithName("APIManager"),
		clientset.Discovery(), recorder)
	// Create a ReconcileMemcached object with the scheme and fake client.
	r := &appscontrollers.APIManagerReconciler{
		BaseReconciler: baseReconciler,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}

	endLoop := false
	for i := 0; i < 100 && !endLoop; i++ {
		res, err := r.Reconcile(req)
		if err != nil {
			t.Fatal(err)
		}

		endLoop = !res.Requeue
	}

	if !endLoop {
		t.Fatal("reconcile did not finish end of reconciliation as expected. APIManager should have been reconciled at this point")
	}

	finalAPIManager := &appsv1alpha1.APIManager{}
	err = r.Client().Get(context.TODO(), req.NamespacedName, finalAPIManager)
	if err != nil {
		t.Fatalf("get APIManager: (%v)", err)
	}

	backendListenerExistingReplicas := finalAPIManager.Spec.Backend.ListenerSpec.Replicas
	if backendListenerExistingReplicas == nil {
		t.Errorf("APIManager's backend listener replicas does not have a default value set")

	}

	if *backendListenerExistingReplicas != 1 {
		t.Errorf("APIManager's backend listener replicas size (%d) is not the expected size (%d)", backendListenerExistingReplicas, 1)
	}
}
