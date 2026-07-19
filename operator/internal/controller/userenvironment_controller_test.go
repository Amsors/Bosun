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
	"errors"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

const testPlatformNamespace = "bosun-platform-test"

var (
	testClient client.Client
	testScheme *runtime.Scheme
	testEnv    *envtest.Environment
)

func TestMain(m *testing.M) {
	testScheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	if err := bosunv1alpha1.AddToScheme(testScheme); err != nil {
		panic(err)
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		panic(err)
	}
	testClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic(err)
	}
	if err := testClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testPlatformNamespace},
	}); err != nil {
		panic(err)
	}

	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		panic(err)
	}
	os.Exit(code)
}

func TestUserEnvironmentReconcileCreatesSecurityBaselineAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	environment := createTestEnvironment(t, "018f9c6e-1234-7000-8000-abcdef012345")
	reconciler := newTestReconciler()
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: environment.Name}}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var namespace corev1.Namespace
	getObject(t, types.NamespacedName{Name: environment.Spec.Namespace}, &namespace)
	assertManagedObject(t, &namespace, environment)

	var quota corev1.ResourceQuota
	getObject(t, namespacedName(environment.Spec.Namespace, resourceQuotaName), &quota)
	assertManagedObject(t, &quota, environment)
	wantQuota := corev1.ResourceList{
		corev1.ResourcePods:            resource.MustParse("2"),
		corev1.ResourceLimitsCPU:       resource.MustParse("2"),
		corev1.ResourceLimitsMemory:    resource.MustParse("3Gi"),
		corev1.ResourceRequestsStorage: resource.MustParse("15Gi"),
	}
	assertResourceListEqual(t, quota.Spec.Hard, wantQuota)

	var limitRange corev1.LimitRange
	getObject(t, namespacedName(environment.Spec.Namespace, limitRangeName), &limitRange)
	assertManagedObject(t, &limitRange, environment)
	if len(limitRange.Spec.Limits) != 1 || limitRange.Spec.Limits[0].Type != corev1.LimitTypeContainer {
		t.Fatalf("LimitRange limits = %#v, want one Container default", limitRange.Spec.Limits)
	}
	assertResourceListEqual(t, limitRange.Spec.Limits[0].DefaultRequest, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	})
	assertResourceListEqual(t, limitRange.Spec.Limits[0].Default, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	})

	var defaultDeny networkingv1.NetworkPolicy
	getObject(t, namespacedName(environment.Spec.Namespace, defaultDenyPolicyName), &defaultDeny)
	assertManagedObject(t, &defaultDeny, environment)
	if len(defaultDeny.Spec.Ingress) != 0 || len(defaultDeny.Spec.Egress) != 0 {
		t.Fatalf("default deny rules = ingress %#v egress %#v, want empty", defaultDeny.Spec.Ingress, defaultDeny.Spec.Egress)
	}
	if len(defaultDeny.Spec.PolicyTypes) != 2 ||
		defaultDeny.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress ||
		defaultDeny.Spec.PolicyTypes[1] != networkingv1.PolicyTypeEgress {
		t.Fatalf("default deny policyTypes = %v", defaultDeny.Spec.PolicyTypes)
	}

	var egress networkingv1.NetworkPolicy
	getObject(t, namespacedName(environment.Spec.Namespace, allowedEgressPolicy), &egress)
	assertManagedObject(t, &egress, environment)
	assertAllowedEgress(t, &egress)

	var roleBinding rbacv1.RoleBinding
	getObject(t, namespacedName(environment.Spec.Namespace, backendRoleBindingName), &roleBinding)
	assertManagedObject(t, &roleBinding, environment)
	if roleBinding.RoleRef != (rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName,
		Kind:     "ClusterRole",
		Name:     backendClusterRoleName,
	}) {
		t.Fatalf("RoleBinding roleRef = %#v", roleBinding.RoleRef)
	}
	if len(roleBinding.Subjects) != 1 ||
		roleBinding.Subjects[0].Kind != "ServiceAccount" ||
		roleBinding.Subjects[0].Name != backendServiceAccount ||
		roleBinding.Subjects[0].Namespace != testPlatformNamespace {
		t.Fatalf("RoleBinding subjects = %#v", roleBinding.Subjects)
	}

	var ready bosunv1alpha1.UserEnvironment
	getObject(t, types.NamespacedName{Name: environment.Name}, &ready)
	if ready.Status.Phase != bosunv1alpha1.UserEnvironmentPhaseReady {
		t.Fatalf("status.phase = %q, want Ready", ready.Status.Phase)
	}
	if ready.Status.ObservedGeneration != ready.Generation {
		t.Fatalf("observedGeneration = %d, want %d", ready.Status.ObservedGeneration, ready.Generation)
	}
	condition := apimeta.FindStatusCondition(ready.Status.Conditions, readyConditionType)
	if condition == nil ||
		condition.Status != metav1.ConditionTrue ||
		condition.Reason != "EnvironmentReady" ||
		condition.ObservedGeneration != ready.Generation {
		t.Fatalf("Ready condition = %#v", condition)
	}

	before := objectVersions(t, environment)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	after := objectVersions(t, environment)
	for name, version := range before {
		if after[name] != version {
			t.Errorf("%s resourceVersion changed on idempotent reconcile: %s -> %s", name, version, after[name])
		}
	}
}

func TestUserEnvironmentReconcileRepairsManagedDrift(t *testing.T) {
	ctx := context.Background()
	environment := createTestEnvironment(t, "018f9c6e-1234-7000-8000-abcdef012346")
	reconciler := newTestReconciler()
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: environment.Name}}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("initial Reconcile() error = %v", err)
	}

	var quota corev1.ResourceQuota
	getObject(t, namespacedName(environment.Spec.Namespace, resourceQuotaName), &quota)
	quota.Spec.Hard[corev1.ResourceLimitsCPU] = resource.MustParse("1")
	if err := testClient.Update(ctx, &quota); err != nil {
		t.Fatalf("Update(ResourceQuota) error = %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("repair Reconcile() error = %v", err)
	}
	getObject(t, namespacedName(environment.Spec.Namespace, resourceQuotaName), &quota)
	cpuLimit := quota.Spec.Hard[corev1.ResourceLimitsCPU]
	if cpuLimit.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("limits.cpu = %s, want 2", cpuLimit.String())
	}
}

func TestUserEnvironmentReconcileReportsResourceConflict(t *testing.T) {
	ctx := context.Background()
	environment := createTestEnvironment(t, "018f9c6e-1234-7000-8000-abcdef012347")
	conflictingNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: environment.Spec.Namespace,
			Labels: map[string]string{
				managedByLabel: "another-controller",
				userLabel:      "018f9c6e-1234-7000-8000-abcdef099999",
			},
		},
	}
	if err := testClient.Create(ctx, conflictingNamespace); err != nil {
		t.Fatalf("Create(conflicting Namespace) error = %v", err)
	}

	result, err := newTestReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: environment.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want positive retry", result.RequeueAfter)
	}

	var failed bosunv1alpha1.UserEnvironment
	getObject(t, types.NamespacedName{Name: environment.Name}, &failed)
	if failed.Status.Phase != bosunv1alpha1.UserEnvironmentPhaseFailed {
		t.Fatalf("status.phase = %q, want Failed", failed.Status.Phase)
	}
	condition := apimeta.FindStatusCondition(failed.Status.Conditions, readyConditionType)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != resourceConflictReason {
		t.Fatalf("Ready condition = %#v", condition)
	}

	var current corev1.Namespace
	getObject(t, types.NamespacedName{Name: environment.Spec.Namespace}, &current)
	if current.Labels[managedByLabel] != "another-controller" {
		t.Fatalf("controller took over conflicting Namespace: labels = %v", current.Labels)
	}
}

func TestUserEnvironmentReconcileReportsInvalidSpec(t *testing.T) {
	ctx := context.Background()
	userID := "018f9c6e-1234-7000-8000-abcdef012348"
	environment := &bosunv1alpha1.UserEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "usr-valid-but-wrong",
			Labels: map[string]string{
				managedByLabel: managedByValue,
				userLabel:      userID,
			},
		},
		Spec: bosunv1alpha1.UserEnvironmentSpec{
			UserID:       userID,
			Namespace:    "bosun-u-" + shortUserID(userID),
			QuotaProfile: quotaProfileMVP,
		},
	}
	if err := testClient.Create(ctx, environment); err != nil {
		t.Fatalf("Create(UserEnvironment) error = %v", err)
	}

	if _, err := newTestReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: environment.Name},
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var failed bosunv1alpha1.UserEnvironment
	getObject(t, types.NamespacedName{Name: environment.Name}, &failed)
	if failed.Status.Phase != bosunv1alpha1.UserEnvironmentPhaseFailed {
		t.Fatalf("status.phase = %q, want Failed", failed.Status.Phase)
	}
	condition := apimeta.FindStatusCondition(failed.Status.Conditions, readyConditionType)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidSpec" {
		t.Fatalf("Ready condition = %#v", condition)
	}

	var namespace corev1.Namespace
	err := testClient.Get(ctx, types.NamespacedName{Name: environment.Spec.Namespace}, &namespace)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("invalid spec created Namespace or returned unexpected error: %v", err)
	}
}

func TestUserEnvironmentStatusRejectsStaleGeneration(t *testing.T) {
	ctx := context.Background()
	environment := createTestEnvironment(t, "018f9c6e-1234-7000-8000-abcdef012349")
	staleGeneration := environment.Generation
	environment.Spec.Namespace = "bosun-u-aaaaaaaaaaaa"
	if err := testClient.Update(ctx, environment); err != nil {
		t.Fatalf("Update(UserEnvironment) error = %v", err)
	}

	err := newTestReconciler().updateStatus(
		ctx,
		types.NamespacedName{Name: environment.Name},
		staleGeneration,
		bosunv1alpha1.UserEnvironmentPhaseReady,
		"EnvironmentReady",
		"must not be written",
	)
	if !errors.Is(err, errGenerationChanged) {
		t.Fatalf("updateStatus() error = %v, want errGenerationChanged", err)
	}

	var current bosunv1alpha1.UserEnvironment
	getObject(t, types.NamespacedName{Name: environment.Name}, &current)
	if current.Status.Phase != "" || len(current.Status.Conditions) != 0 {
		t.Fatalf("stale status update was persisted: %#v", current.Status)
	}
}

func newTestReconciler() *UserEnvironmentReconciler {
	return &UserEnvironmentReconciler{
		Client:            testClient,
		Scheme:            testScheme,
		PlatformNamespace: testPlatformNamespace,
	}
}

func createTestEnvironment(t *testing.T, userID string) *bosunv1alpha1.UserEnvironment {
	t.Helper()
	shortID := shortUserID(userID)
	environment := &bosunv1alpha1.UserEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "usr-" + shortID,
			Labels: map[string]string{
				managedByLabel: managedByValue,
				userLabel:      userID,
			},
		},
		Spec: bosunv1alpha1.UserEnvironmentSpec{
			UserID:       userID,
			Namespace:    "bosun-u-" + shortID,
			QuotaProfile: quotaProfileMVP,
		},
	}
	if err := testClient.Create(context.Background(), environment); err != nil {
		t.Fatalf("Create(UserEnvironment) error = %v", err)
	}
	return environment
}

func getObject(t *testing.T, key types.NamespacedName, object client.Object) {
	t.Helper()
	if err := testClient.Get(context.Background(), key, object); err != nil {
		t.Fatalf("Get(%T %s) error = %v", object, key, err)
	}
}

func namespacedName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

func assertManagedObject(
	t *testing.T,
	object client.Object,
	environment *bosunv1alpha1.UserEnvironment,
) {
	t.Helper()
	if object.GetLabels()[managedByLabel] != managedByValue ||
		object.GetLabels()[userLabel] != environment.Spec.UserID {
		t.Fatalf("%T labels = %v", object, object.GetLabels())
	}
	owner := metav1.GetControllerOf(object)
	if owner == nil || owner.APIVersion != bosunv1alpha1.SchemeGroupVersion.String() ||
		owner.Kind != userEnvironmentKind || owner.Name != environment.Name ||
		owner.UID != environment.UID {
		t.Fatalf("%T controller owner = %#v", object, owner)
	}
}

func assertResourceListEqual(t *testing.T, got, want corev1.ResourceList) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ResourceList length = %d, want %d: got %v", len(got), len(want), got)
	}
	for name, quantity := range want {
		gotQuantity, ok := got[name]
		if !ok || gotQuantity.Cmp(quantity) != 0 {
			t.Fatalf("ResourceList[%s] = %s, want %s", name, gotQuantity.String(), quantity.String())
		}
	}
}

func assertAllowedEgress(t *testing.T, policy *networkingv1.NetworkPolicy) {
	t.Helper()
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("allowed egress policyTypes = %v", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 3 {
		t.Fatalf("allowed egress rules = %d, want 3", len(policy.Spec.Egress))
	}

	dns := policy.Spec.Egress[0]
	if len(dns.To) != 1 ||
		dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" ||
		dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" ||
		len(dns.Ports) != 2 ||
		dns.Ports[0].Port.IntValue() != 53 ||
		dns.Ports[1].Port.IntValue() != 53 {
		t.Fatalf("DNS egress rule = %#v", dns)
	}

	platform := policy.Spec.Egress[1]
	if len(platform.To) != 1 ||
		platform.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != testPlatformNamespace ||
		platform.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "bosun-gateway" ||
		len(platform.Ports) != 1 ||
		platform.Ports[0].Port.IntValue() != 8081 {
		t.Fatalf("platform egress rule = %#v", platform)
	}

	proxy := policy.Spec.Egress[2]
	if len(proxy.To) != 1 ||
		proxy.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != testPlatformNamespace ||
		proxy.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "bosun-egress-proxy" ||
		len(proxy.Ports) != 1 ||
		proxy.Ports[0].Port.IntValue() != 3128 {
		t.Fatalf("proxy egress rule = %#v", proxy)
	}
}

func objectVersions(t *testing.T, environment *bosunv1alpha1.UserEnvironment) map[string]string {
	t.Helper()
	objects := map[string]client.Object{
		userEnvironmentKind: &bosunv1alpha1.UserEnvironment{},
		"Namespace":         &corev1.Namespace{},
		"ResourceQuota":     &corev1.ResourceQuota{},
		"LimitRange":        &corev1.LimitRange{},
		"DefaultDeny":       &networkingv1.NetworkPolicy{},
		"AllowedEgress":     &networkingv1.NetworkPolicy{},
		"RoleBinding":       &rbacv1.RoleBinding{},
	}
	keys := map[string]types.NamespacedName{
		userEnvironmentKind: {Name: environment.Name},
		"Namespace":         {Name: environment.Spec.Namespace},
		"ResourceQuota":     namespacedName(environment.Spec.Namespace, resourceQuotaName),
		"LimitRange":        namespacedName(environment.Spec.Namespace, limitRangeName),
		"DefaultDeny":       namespacedName(environment.Spec.Namespace, defaultDenyPolicyName),
		"AllowedEgress":     namespacedName(environment.Spec.Namespace, allowedEgressPolicy),
		"RoleBinding":       namespacedName(environment.Spec.Namespace, backendRoleBindingName),
	}
	versions := make(map[string]string, len(objects))
	for name, object := range objects {
		getObject(t, keys[name], object)
		versions[name] = object.GetResourceVersion()
	}
	return versions
}
