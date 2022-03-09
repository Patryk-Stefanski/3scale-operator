/*
Copyright 2020 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	capabilitiesv1beta1 "github.com/3scale/3scale-operator/apis/capabilities/v1beta1"
	controllerhelper "github.com/3scale/3scale-operator/pkg/controller/helper"
	"github.com/3scale/3scale-operator/pkg/helper"
	"github.com/3scale/3scale-operator/pkg/reconcilers"
	"github.com/3scale/3scale-operator/version"
)

// BackendReconciler reconciles a Backend object
type BackendReconciler struct {
	*reconcilers.BaseReconciler
}

const backendFinalizer = "backend.capabilities.3scale.net/finalizer"

// blank assignment to verify that BackendReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &BackendReconciler{}

// +kubebuilder:rbac:groups=capabilities.3scale.net,namespace=placeholder,resources=backends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capabilities.3scale.net,namespace=placeholder,resources=backends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capabilities.3scale.net,namespace=placeholder,resources=backends/finalizers,verbs=get;list;watch;create;update;patch;delete

func (r *BackendReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	reqLogger := r.Logger().WithValues("backend", req.NamespacedName)
	reqLogger.Info("Reconcile Backend", "Operator version", version.Version)

	// Fetch the Backend instance
	backend := &capabilitiesv1beta1.Backend{}
	err := r.Client().Get(context.TODO(), req.NamespacedName, backend)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			reqLogger.Info("resource not found. Ignoring since object must have been deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	if reqLogger.V(1).Enabled() {
		jsonData, err := json.MarshalIndent(backend, "", "  ")
		if err != nil {
			return ctrl.Result{}, err
		}
		reqLogger.V(1).Info(string(jsonData))
	}

	// Ignore deleted Backends, this can happen when foregroundDeletion is enabled
	// https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/#foreground-cascading-deletion
	if backend.GetDeletionTimestamp() != nil && controllerutil.ContainsFinalizer(backend, backendFinalizer) {
		err = r.removeBackend(backend)
		if err != nil {
			return ctrl.Result{}, err
		}

		err = controllerhelper.ReconcileFinalizers(backend, r.Client(), backendFinalizer)
		if err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Ignore deleted Backends, this can happen when foregroundDeletion is enabled
	// https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/#foreground-cascading-deletion
	if backend.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(backend, backendFinalizer) {
		err = controllerhelper.ReconcileFinalizers(backend, r.Client(), backendFinalizer)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if backend.SetDefaults(reqLogger) {
		err := r.Client().Update(r.Context(), backend)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("Failed setting backend defaults: %w", err)
		}

		reqLogger.Info("resource defaults updated. Requeueing.")
		return ctrl.Result{Requeue: true}, nil
	}

	statusReconciler, reconcileErr := r.reconcile(backend)
	statusResult, statusUpdateErr := statusReconciler.Reconcile()
	if statusUpdateErr != nil {
		if reconcileErr != nil {
			return ctrl.Result{}, fmt.Errorf("Failed to sync backend: %v. Failed to update backend status: %w", reconcileErr, statusUpdateErr)
		}

		return ctrl.Result{}, fmt.Errorf("Failed to update backend status: %w", statusUpdateErr)
	}

	if statusResult.Requeue {
		return statusResult, nil
	}

	if reconcileErr != nil {
		if helper.IsInvalidSpecError(reconcileErr) {
			// On Validation error, no need to retry as spec is not valid and needs to be changed
			reqLogger.Info("ERROR", "spec validation error", reconcileErr)
			r.EventRecorder().Eventf(backend, corev1.EventTypeWarning, "Invalid Backend Spec", "%v", reconcileErr)
			return ctrl.Result{}, nil
		}

		reqLogger.Error(reconcileErr, "Failed to reconcile")
		r.EventRecorder().Eventf(backend, corev1.EventTypeWarning, "ReconcileError", "%v", reconcileErr)
		return ctrl.Result{}, reconcileErr
	}

	reqLogger.Info("END", "error", reconcileErr)
	return ctrl.Result{}, nil
}

func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capabilitiesv1beta1.Backend{}).
		Complete(r)
}

func (r *BackendReconciler) reconcile(backendResource *capabilitiesv1beta1.Backend) (*BackendStatusReconciler, error) {
	logger := r.Logger().WithValues("backend", backendResource.Name)

	err := r.validateSpec(backendResource)
	if err != nil {
		statusReconciler := NewBackendStatusReconciler(r.BaseReconciler, backendResource, nil, "", err)
		return statusReconciler, err
	}

	providerAccount, err := controllerhelper.LookupProviderAccount(r.Client(), backendResource.Namespace, backendResource.Spec.ProviderAccountRef, logger)
	if err != nil {
		statusReconciler := NewBackendStatusReconciler(r.BaseReconciler, backendResource, nil, "", err)
		return statusReconciler, err
	}

	threescaleAPIClient, err := controllerhelper.PortaClient(providerAccount)
	if err != nil {
		statusReconciler := NewBackendStatusReconciler(r.BaseReconciler, backendResource, nil, providerAccount.AdminURLStr, err)
		return statusReconciler, err
	}

	backendRemoteIndex, err := controllerhelper.NewBackendAPIRemoteIndex(threescaleAPIClient, logger)
	if err != nil {
		statusReconciler := NewBackendStatusReconciler(r.BaseReconciler, backendResource, nil, providerAccount.AdminURLStr, err)
		return statusReconciler, err
	}

	reconciler := NewThreescaleReconciler(r.BaseReconciler, backendResource, threescaleAPIClient, backendRemoteIndex, providerAccount)
	backendAPIEntity, err := reconciler.Reconcile()
	statusReconciler := NewBackendStatusReconciler(r.BaseReconciler, backendResource, backendAPIEntity, providerAccount.AdminURLStr, err)
	return statusReconciler, err
}

func (r *BackendReconciler) validateSpec(backendResource *capabilitiesv1beta1.Backend) error {
	errors := field.ErrorList{}
	// internal validation
	errors = append(errors, backendResource.Validate()...)

	if len(errors) == 0 {
		return nil
	}

	return &helper.SpecFieldError{
		ErrorType:      helper.InvalidError,
		FieldErrorList: errors,
	}
}

func (r *BackendReconciler) removeBackend(backend *capabilitiesv1beta1.Backend) error {
	providerAccount, err := controllerhelper.LookupProviderAccount(r.Client(), backend.Namespace, backend.Spec.ProviderAccountRef, r.Logger())
	if err != nil {
		return err
	}

	threescaleAPIClient, err := controllerhelper.PortaClient(providerAccount)
	if err != nil {
		return err
	}

	// Retrieve all product CRs that are under the same ns as the backend CR
	opts := k8sclient.ListOptions{
		Namespace: backend.Namespace,
	}
	productCRsList := &capabilitiesv1beta1.ProductList{}
	err = r.Client().List(context.TODO(), productCRsList, &opts)
	if err != nil {
		return err
	}

	// fetch CRs that belong to a tenant
	tenantProductCRs, err := r.fetchTenantProductCRs(productCRsList, backend)

	// update backendUsages for each product retrieved
	for _, productCR := range tenantProductCRs {
		delete(productCR.Spec.BackendUsages, backend.Spec.SystemName)
		err = r.Client().Update(context.TODO(), &productCR)
		if err != nil {
			return err
		}
	}

	// Attempt to remove backendAPI - expect error on first attempt as the backendUsage has not been removed yet from 3scale
	err = threescaleAPIClient.DeleteBackendApi(*backend.Status.ID)
	if err != nil {
		return err
	}

	return nil
}

func (r *BackendReconciler) fetchTenantProductCRs(productsCRsList *capabilitiesv1beta1.ProductList, backend *capabilitiesv1beta1.Backend) ([]capabilitiesv1beta1.Product, error) {
	var productsList []capabilitiesv1beta1.Product
	backendProviderAccount, err := controllerhelper.LookupProviderAccount(r.Client(), backend.Namespace, backend.Spec.ProviderAccountRef, r.Logger())
	if err != nil {
		return nil, err
	}
	
	// range through productCRs 
	for _, productCR := range productsCRsList.Items {
		// retrieve productCR providerAccount
		productProviderAccount, err := controllerhelper.LookupProviderAccount(r.Client(), productCR.Namespace, productCR.Spec.ProviderAccountRef, r.Logger())
		if err != nil {
			// skip product CR if productProviderAccount is not found
			continue
		}

		if backendProviderAccount.AdminURLStr == productProviderAccount.AdminURLStr {
			if _, ok := productCR.Spec.BackendUsages[backend.Spec.SystemName]; ok {
				productsList = append(productsList, productCR)
			}
		}
	}

	return productsList, nil
}