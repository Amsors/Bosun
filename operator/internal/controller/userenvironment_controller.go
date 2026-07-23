/*
Copyright 2026.

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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

const (
	managedByLabel         = "app.kubernetes.io/managed-by"
	userLabel              = "bosun.io/user"
	managedByValue         = "bosun"
	resourceQuotaName      = "bosun-user-quota"
	limitRangeName         = "bosun-container-defaults"
	defaultDenyPolicyName  = "bosun-default-deny"
	allowedEgressPolicy    = "bosun-allowed-egress"
	backendRoleBindingName = "bosun-backend-access"
	backendClusterRoleName = "bosun-user-backend-terminal"
	backendServiceAccount  = "bosun-backend-api"
	readyConditionType     = "Ready"
	resourceConflictReason = "ResourceConflict"
	quotaProfileMVP        = "mvp"
	userEnvironmentKind    = "UserEnvironment"
)

var userIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var errGenerationChanged = errors.New("UserEnvironment generation changed during reconciliation")

type permanentReconcileError struct {
	reason  string
	message string
}

func (e *permanentReconcileError) Error() string {
	return e.message
}

// UserEnvironmentReconciler reconciles a UserEnvironment object.
type UserEnvironmentReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	PlatformNamespace string
}

// +kubebuilder:rbac:groups=bosun.io,resources=userenvironments,verbs=get;list;watch
// +kubebuilder:rbac:groups=bosun.io,resources=userenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bosun.io,resources=userenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;resourcequotas;limitranges,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch

// Reconcile prepares the namespace security baseline for one UserEnvironment.
func (r *UserEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var environment bosunv1alpha1.UserEnvironment
	if err := r.Get(ctx, req.NamespacedName, &environment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get UserEnvironment %s: %w", req.Name, err)
	}

	if err := validateUserEnvironment(&environment); err != nil {
		if statusErr := r.updateStatus(
			ctx,
			req.NamespacedName,
			environment.Generation,
			bosunv1alpha1.UserEnvironmentPhaseFailed,
			"InvalidSpec",
			err.Error(),
		); statusErr != nil {
			return resultForStatusError(statusErr)
		}
		log.Info("Rejected UserEnvironment with invalid spec", "name", environment.Name, "reason", err)
		return ctrl.Result{}, nil
	}

	if environment.Status.Phase == "" || environment.Status.ObservedGeneration != environment.Generation {
		if err := r.updateStatus(
			ctx,
			req.NamespacedName,
			environment.Generation,
			bosunv1alpha1.UserEnvironmentPhasePending,
			"Provisioning",
			"User environment security baseline is being prepared",
		); err != nil {
			return resultForStatusError(err)
		}
	}

	if err := r.ensureBaseline(ctx, &environment); err != nil {
		var permanentErr *permanentReconcileError
		if errors.As(err, &permanentErr) {
			if statusErr := r.updateStatus(
				ctx,
				req.NamespacedName,
				environment.Generation,
				bosunv1alpha1.UserEnvironmentPhaseFailed,
				permanentErr.reason,
				permanentErr.message,
			); statusErr != nil {
				return resultForStatusError(statusErr)
			}
			log.Info(
				"UserEnvironment security baseline has a resource conflict",
				"name", environment.Name,
				"reason", permanentErr.message,
			)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		if statusErr := r.updateStatus(
			ctx,
			req.NamespacedName,
			environment.Generation,
			bosunv1alpha1.UserEnvironmentPhasePending,
			"ReconcileError",
			"Could not prepare the user environment security baseline",
		); statusErr != nil {
			if errors.Is(statusErr, errGenerationChanged) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, errors.Join(err, statusErr)
		}
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(
		ctx,
		req.NamespacedName,
		environment.Generation,
		bosunv1alpha1.UserEnvironmentPhaseReady,
		"EnvironmentReady",
		"User environment security baseline is ready",
	); err != nil {
		return resultForStatusError(err)
	}

	log.Info("UserEnvironment security baseline is ready", "name", environment.Name, "namespace", environment.Spec.Namespace)
	return ctrl.Result{}, nil
}

func (r *UserEnvironmentReconciler) ensureBaseline(
	ctx context.Context,
	environment *bosunv1alpha1.UserEnvironment,
) error {
	objects := []struct {
		object client.Object
		mutate func() error
	}{
		{
			object: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: environment.Spec.Namespace}},
			mutate: func() error { return nil },
		},
	}

	for _, item := range objects {
		if err := r.createOrUpdateOwned(ctx, environment, item.object, item.mutate); err != nil {
			return err
		}
	}

	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{
		Name:      resourceQuotaName,
		Namespace: environment.Spec.Namespace,
	}}
	if err := r.createOrUpdateOwned(ctx, environment, quota, func() error {
		quota.Spec = corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods:            resource.MustParse("3"),
				corev1.ResourceLimitsCPU:       resource.MustParse("3"),
				corev1.ResourceLimitsMemory:    resource.MustParse("6Gi"),
				corev1.ResourceRequestsStorage: resource.MustParse("30Gi"),
			},
		}
		return nil
	}); err != nil {
		return err
	}

	limitRange := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{
		Name:      limitRangeName,
		Namespace: environment.Spec.Namespace,
	}}
	if err := r.createOrUpdateOwned(ctx, environment, limitRange, func() error {
		limitRange.Spec.Limits = []corev1.LimitRangeItem{{
			Type: corev1.LimitTypeContainer,
			DefaultRequest: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Default: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}}
		return nil
	}); err != nil {
		return err
	}

	defaultDeny := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name:      defaultDenyPolicyName,
		Namespace: environment.Spec.Namespace,
	}}
	if err := r.createOrUpdateOwned(ctx, environment, defaultDeny, func() error {
		defaultDeny.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		}
		return nil
	}); err != nil {
		return err
	}

	allowedEgress := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name:      allowedEgressPolicy,
		Namespace: environment.Spec.Namespace,
	}}
	if err := r.createOrUpdateOwned(ctx, environment, allowedEgress, func() error {
		tcp := corev1.ProtocolTCP
		udp := corev1.ProtocolUDP
		dnsPort := intstr.FromInt32(53)
		gatewayPort := intstr.FromInt32(8081)
		proxyPort := intstr.FromInt32(3128)
		allowedEgress.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: namespaceSelector("kube-system"),
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"k8s-app": "kube-dns"},
						},
					}},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udp, Port: &dnsPort},
						{Protocol: &tcp, Port: &dnsPort},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: namespaceSelector(r.PlatformNamespace),
						PodSelector:       podSelector("bosun-gateway"),
					}},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &gatewayPort},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: namespaceSelector(r.PlatformNamespace),
						PodSelector:       podSelector("bosun-egress-proxy"),
					}},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &proxyPort},
					},
				},
			},
		}
		return nil
	}); err != nil {
		return err
	}

	roleBinding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name:      backendRoleBindingName,
		Namespace: environment.Spec.Namespace,
	}}
	if err := r.createOrUpdateOwned(ctx, environment, roleBinding, func() error {
		desiredRoleRef := rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     backendClusterRoleName,
		}
		if roleBinding.ResourceVersion != "" && roleBinding.RoleRef != desiredRoleRef {
			return &permanentReconcileError{
				reason:  resourceConflictReason,
				message: fmt.Sprintf("RoleBinding %s/%s has an incompatible roleRef", roleBinding.Namespace, roleBinding.Name),
			}
		}
		roleBinding.RoleRef = desiredRoleRef
		roleBinding.Subjects = []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      backendServiceAccount,
			Namespace: r.PlatformNamespace,
		}}
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (r *UserEnvironmentReconciler) createOrUpdateOwned(
	ctx context.Context,
	environment *bosunv1alpha1.UserEnvironment,
	object client.Object,
	mutate func() error,
) error {
	key := client.ObjectKeyFromObject(object)
	existing := object.DeepCopyObject().(client.Object)
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get %T %s: %w", object, key, err)
		}
	} else if err := validateManagedObject(existing, environment.Spec.UserID); err != nil {
		return err
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, object, func() error {
		labels := object.GetLabels()
		if labels == nil {
			labels = make(map[string]string, 2)
		}
		labels[managedByLabel] = managedByValue
		labels[userLabel] = environment.Spec.UserID
		object.SetLabels(labels)

		if err := controllerutil.SetControllerReference(environment, object, r.Scheme); err != nil {
			return &permanentReconcileError{
				reason:  resourceConflictReason,
				message: fmt.Sprintf("%T %s cannot be owned by UserEnvironment %s: %v", object, key, environment.Name, err),
			}
		}
		return mutate()
	})
	if err != nil {
		var permanentErr *permanentReconcileError
		if errors.As(err, &permanentErr) {
			return permanentErr
		}
		return fmt.Errorf("reconcile %T %s: %w", object, key, err)
	}
	return nil
}

func validateManagedObject(object client.Object, userID string) error {
	labels := object.GetLabels()
	if labels[managedByLabel] != managedByValue || labels[userLabel] != userID {
		return &permanentReconcileError{
			reason: resourceConflictReason,
			message: fmt.Sprintf(
				"%T %s is not managed by Bosun for user %s",
				object,
				client.ObjectKeyFromObject(object),
				userID,
			),
		}
	}
	return nil
}

func validateUserEnvironment(environment *bosunv1alpha1.UserEnvironment) error {
	if !userIDPattern.MatchString(environment.Spec.UserID) {
		return fmt.Errorf("spec.userID must be a UUID v7")
	}
	if environment.Labels[managedByLabel] != managedByValue {
		return fmt.Errorf("metadata.labels[%s] must be %s", managedByLabel, managedByValue)
	}
	if environment.Labels[userLabel] != environment.Spec.UserID {
		return fmt.Errorf("metadata.labels[%s] must match spec.userID", userLabel)
	}
	shortID := shortUserID(environment.Spec.UserID)
	expectedName := "usr-" + shortID
	if environment.Name != expectedName {
		return fmt.Errorf("metadata.name must be %s for spec.userID", expectedName)
	}
	expectedNamespace := "bosun-u-" + shortID
	if environment.Spec.Namespace != expectedNamespace {
		return fmt.Errorf("spec.namespace must be %s for spec.userID", expectedNamespace)
	}
	if environment.Spec.QuotaProfile != quotaProfileMVP {
		return fmt.Errorf("spec.quotaProfile must be mvp")
	}
	return nil
}

func shortUserID(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:12]
}

func namespaceSelector(namespace string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
	}
}

func podSelector(name string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{"app.kubernetes.io/name": name},
	}
}

func (r *UserEnvironmentReconciler) updateStatus(
	ctx context.Context,
	key types.NamespacedName,
	expectedGeneration int64,
	phase bosunv1alpha1.UserEnvironmentPhase,
	reason string,
	message string,
) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current bosunv1alpha1.UserEnvironment
		if err := r.Get(ctx, key, &current); err != nil {
			return fmt.Errorf("refresh UserEnvironment %s before status update: %w", key.Name, err)
		}
		if current.Generation != expectedGeneration {
			return errGenerationChanged
		}

		desired := current.Status.DeepCopy()
		desired.Phase = phase
		desired.ObservedGeneration = current.Generation
		conditionStatus := metav1.ConditionFalse
		if phase == bosunv1alpha1.UserEnvironmentPhaseReady {
			conditionStatus = metav1.ConditionTrue
		}
		apimeta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:               readyConditionType,
			Status:             conditionStatus,
			ObservedGeneration: current.Generation,
			Reason:             reason,
			Message:            message,
		})
		if reflect.DeepEqual(current.Status, *desired) {
			return nil
		}
		current.Status = *desired
		if err := r.Status().Update(ctx, &current); err != nil {
			return fmt.Errorf("update UserEnvironment %s status: %w", key.Name, err)
		}
		return nil
	})
}

func resultForStatusError(err error) (ctrl.Result, error) {
	if errors.Is(err, errGenerationChanged) {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.PlatformNamespace == "" {
		return fmt.Errorf("platform namespace must not be empty")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&bosunv1alpha1.UserEnvironment{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&corev1.LimitRange{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("userenvironment").
		Complete(r)
}
