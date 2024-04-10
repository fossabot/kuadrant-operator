/*
Copyright 2021 Red Hat, Inc.

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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	gatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	kuadrantv1beta2 "github.com/kuadrant/kuadrant-operator/api/v1beta2"
	kuadrantgatewayapi "github.com/kuadrant/kuadrant-operator/pkg/library/gatewayapi"
	"github.com/kuadrant/kuadrant-operator/pkg/library/kuadrant"
	"github.com/kuadrant/kuadrant-operator/pkg/library/reconcilers"
	"github.com/kuadrant/kuadrant-operator/pkg/library/utils"
	"github.com/kuadrant/kuadrant-operator/pkg/rlptools"
)

// RateLimitPolicyReconciler reconciles a RateLimitPolicy object
type RateLimitPolicyReconciler struct {
	*reconcilers.BaseReconciler
}

//+kubebuilder:rbac:groups=kuadrant.io,resources=ratelimitpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuadrant.io,resources=ratelimitpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuadrant.io,resources=ratelimitpolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the RateLimitPolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *RateLimitPolicyReconciler) Reconcile(eventCtx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger().WithValues("RateLimitPolicy", req.NamespacedName)
	logger.Info("Reconciling RateLimitPolicy")
	ctx := logr.NewContext(eventCtx, logger)

	// fetch the ratelimitpolicy
	rlp := &kuadrantv1beta2.RateLimitPolicy{}
	if err := r.Client().Get(ctx, req.NamespacedName, rlp); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("no RateLimitPolicy found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get RateLimitPolicy")
		return ctrl.Result{}, err
	}

	if logger.V(1).Enabled() {
		jsonData, err := json.MarshalIndent(rlp, "", "  ")
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.V(1).Info(string(jsonData))
	}

	// Ignore deleted resources, this can happen when foregroundDeletion is enabled
	// https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/#foreground-cascading-deletion
	if rlp.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}

	// perform validations
	validationErr := r.validate(ctx, rlp)

	// reconcile ratelimitpolicy status
	statusResult, statusErr := r.reconcileStatus(ctx, rlp, validationErr)

	if validationErr != nil {
		return ctrl.Result{}, validationErr
	}

	if statusErr != nil {
		return ctrl.Result{}, statusErr
	}

	if statusResult.Requeue {
		logger.V(1).Info("Reconciling status not finished. Requeueing.")
		return statusResult, nil
	}

	logger.Info("RateLimitPolicy reconciled successfully")
	return ctrl.Result{}, nil
}

// validate performs validation before proceeding with the reconcile loop, returning a common.ErrInvalid on failing validation
func (r *RateLimitPolicyReconciler) validate(ctx context.Context, rlp *kuadrantv1beta2.RateLimitPolicy) error {
	if err := rlp.Validate(); err != nil {
		return kuadrant.NewErrInvalid(rlp.Kind(), err)
	}

	targetNetworkObject, err := reconcilers.FetchTargetRefObject(ctx, r.Client(), rlp.GetTargetRef(), rlp.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return kuadrant.NewErrTargetNotFound(rlp.Kind(), rlp.GetTargetRef(), err)
		}
		return kuadrant.NewErrInvalid(rlp.Kind(), err)
	}

	if err := kuadrant.ValidateHierarchicalRules(rlp, targetNetworkObject); err != nil {
		return kuadrant.NewErrInvalid(rlp.Kind(), err)
	}

	// Ensures only one RLP targets the network resource
	// This validation should be removed when (if) kuadrant supports multiple policies
	// targeting the same Gateway API network resource
	if err := r.checkTargetDoublePolicyReference(ctx, rlp); err != nil {
		return err
	}

	return nil
}

func (r *RateLimitPolicyReconciler) checkTargetDoublePolicyReference(ctx context.Context, rlp *kuadrantv1beta2.RateLimitPolicy) error {
	t, err := rlptools.APIGatewayTopology(ctx, r.Client())
	if err != nil {
		return err
	}

	var attachedPolicies []kuadrantgatewayapi.Policy

	if kuadrantgatewayapi.IsTargetRefGateway(rlp.GetTargetRef()) {
		// find the node in the topology to be queried for the attached policies
		gatewayNode, ok := utils.Find(t.Gateways(), func(g kuadrantgatewayapi.GatewayNode) bool {
			return g.ObjectKey() == rlp.TargetKey()
		})
		if !ok {
			return fmt.Errorf("gateway (%v) not found or not ready", rlp.TargetKey())
		}
		attachedPolicies = gatewayNode.AttachedPolicies()
	} else if kuadrantgatewayapi.IsTargetRefHTTPRoute(rlp.GetTargetRef()) {
		// find the node in the topology to be queried for the attached policies
		routeNode, ok := utils.Find(t.Routes(), func(n kuadrantgatewayapi.RouteNode) bool {
			return n.ObjectKey() == rlp.TargetKey()
		})
		if !ok {
			return fmt.Errorf("httproute (%v) not found or not accepted", rlp.TargetKey())
		}
		attachedPolicies = routeNode.AttachedPolicies()
	}

	if len(attachedPolicies) == 0 {
		return fmt.Errorf("targetref (%v) does not have attached policies", rlp.TargetKey())
	}

	if len(attachedPolicies) == 1 {
		// target is not referenced by another policy
		return nil
	}

	// There are at least 2 policies referencing the target network resource
	// It is still ok if the current policy `rlp` "was" the first one targeting the network resource
	// The current implementation considers the RLP as the rightful policy targeting the network
	// resource when
	// A) the status condition is "accepted"
	//     OR
	// B) the status is not accepted and the error is NOT that the target is already being referenced by another policy

	if meta.IsStatusConditionTrue(rlp.Status.Conditions, string(gatewayapiv1alpha2.PolicyConditionAccepted)) {
		return nil
	}

	return kuadrant.NewErrConflict(rlp.Kind(), val, fmt.Errorf("the %s target %s is already referenced by policy %s", targetNetworkObjectKind, targetNetworkObjectKey, val))
}

// SetupWithManager sets up the controller with the Manager.
func (r *RateLimitPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuadrantv1beta2.RateLimitPolicy{}).
		Complete(r)
}
