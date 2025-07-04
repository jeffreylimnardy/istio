package istioresources

import (
	"context"
	"fmt"

	"github.com/kyma-project/istio/operator/api/v1alpha2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kyma-project/istio/operator/internal/clusterconfig"
	"github.com/kyma-project/istio/operator/internal/describederrors"
)

type ResourcesReconciliation interface {
	Reconcile(ctx context.Context, istioCR v1alpha2.Istio) describederrors.DescribedError
}

type ResourcesReconciler struct {
	client         client.Client
	templateValues map[string]string
}

func NewReconciler(client client.Client) *ResourcesReconciler {
	return &ResourcesReconciler{
		client: client,
	}
}

type Resource interface {
	Name() string
	reconcile(ctx context.Context, k8sClient client.Client, owner metav1.OwnerReference, templateValues map[string]string) (controllerutil.OperationResult, error)
}

func (r *ResourcesReconciler) Reconcile(ctx context.Context, istioCR v1alpha2.Istio) describederrors.DescribedError {
	ctrl.Log.Info("Reconciling Istio resources")

	provider, err := clusterconfig.GetClusterProvider(ctx, r.client)
	if err != nil {
		return describederrors.NewDescribedError(err, "could not determine cluster provider")
	}

	resources, err := getResources(r.client, provider)
	if err != nil {
		ctrl.Log.Error(err, "Failed to initialise Istio resources")
		return describederrors.NewDescribedError(err, "Istio controller failed to initialise Istio resources")
	}

	owner := metav1.OwnerReference{
		APIVersion: istioCR.APIVersion,
		Kind:       istioCR.Kind,
		Name:       istioCR.Name,
		UID:        istioCR.UID,
	}

	for _, resource := range resources {
		ctrl.Log.Info("Reconciling Istio resource", "name", resource.Name())
		result, reconcileErr := resource.reconcile(ctx, r.client, owner, r.templateValues)

		if reconcileErr != nil {
			return describederrors.NewDescribedError(reconcileErr, fmt.Sprintf("Could not reconcile Istio resource %s", resource.Name()))
		}
		ctrl.Log.Info("Reconciled Istio resource", "name", resource.Name(), "result", result)
	}

	ctrl.Log.Info("Successfully reconciled Istio resources")

	return nil
}

// getResources returns all Istio resources required for the reconciliation specific for the given hyperscaler.
func getResources(k8sClient client.Client, provider string) ([]Resource, error) {
	istioResources := []Resource{NewPeerAuthenticationMtls(k8sClient)}

	switch provider {
	case clusterconfig.Aws:

		shouldUseNLB, err := clusterconfig.ShouldUseNLB(context.Background(), k8sClient)
		if err != nil {
			return nil, err
		}

		istioResources = append(istioResources, NewProxyProtocolEnvoyFilter(k8sClient, shouldUseNLB))

	case clusterconfig.Openstack:
		// NLB is a default only for AWS clusters so for OpenStack we need to set the usage of NLB to false
		istioResources = append(istioResources, NewProxyProtocolEnvoyFilter(k8sClient, false))

	default:
		return istioResources, nil
	}

	return istioResources, nil
}
