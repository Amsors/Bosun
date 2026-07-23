package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

func TestAgentSessionReconcileCreatesSecureTieredWorkloadAndIsIdempotent(t *testing.T) {
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012401", "018f9c6e-1234-7000-8000-abcdef012501")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)

	assertSmallSessionPVC(t, session)
	assertSessionServiceAccount(t, session)
	pod := getAgentPod(t, session)
	assertSecureSmallSessionPod(t, session, &pod)
	assertAgentSessionPodReconcileIsIdempotent(t, reconciler, session, &pod)
}

func assertSmallSessionPVC(t *testing.T, session *bosunv1alpha1.AgentSession) {
	t.Helper()
	var pvc corev1.PersistentVolumeClaim
	getObject(t, namespacedName(session.Namespace, sessionidentity.PVCName(session.Spec.SessionID)), &pvc)
	assertSessionLabels(t, pvc.Labels, session)
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.Cmp(resource.MustParse("5Gi")) != 0 || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "local-path" {
		t.Fatalf("PVC spec = %#v", pvc.Spec)
	}
}

func assertSessionServiceAccount(t *testing.T, session *bosunv1alpha1.AgentSession) {
	t.Helper()
	var sa corev1.ServiceAccount
	getObject(t, namespacedName(session.Namespace, sessionidentity.ServiceAccountName(session.Spec.SessionID)), &sa)
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Fatal("session ServiceAccount must disable automount")
	}
}

func getAgentPod(t *testing.T, session *bosunv1alpha1.AgentSession) corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	if err := testClient.Get(context.Background(), namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod); err != nil {
		var current bosunv1alpha1.AgentSession
		getObject(t, clientKey(session), &current)
		t.Fatalf("get Pod: %v; phase=%s conditions=%#v", err, current.Status.Phase, current.Status.Conditions)
	}
	return pod
}

func assertSecureSmallSessionPod(
	t *testing.T,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) {
	t.Helper()
	assertSessionLabels(t, pod.Labels, session)
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 28800 {
		t.Fatalf("activeDeadlineSeconds = %v", pod.Spec.ActiveDeadlineSeconds)
	}
	if pod.Spec.SecurityContext == nil ||
		pod.Spec.SecurityContext.RunAsUser == nil || *pod.Spec.SecurityContext.RunAsUser != 10001 ||
		pod.Spec.SecurityContext.RunAsGroup == nil || *pod.Spec.SecurityContext.RunAsGroup != 10001 ||
		pod.Spec.SecurityContext.FSGroup == nil || *pod.Spec.SecurityContext.FSGroup != 10001 {
		t.Fatalf("Pod runtime identity = %#v", pod.Spec.SecurityContext)
	}
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("containers = %d, want agent + auth-proxy", len(pod.Spec.Containers))
	}
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "runtime-init" {
		t.Fatalf("init containers = %#v, want runtime-init", pod.Spec.InitContainers)
	}
	if pod.Spec.Containers[0].ImagePullPolicy != corev1.PullAlways ||
		pod.Spec.Containers[1].ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("agent image pull policies = %q, %q", pod.Spec.Containers[0].ImagePullPolicy, pod.Spec.Containers[1].ImagePullPolicy)
	}
	assertRestrictedContainer(t, pod.Spec.Containers[0])
	assertRestrictedContainer(t, pod.Spec.Containers[1])
	if pod.Spec.Containers[0].Resources.Requests.Cpu().Cmp(resource.MustParse("240m")) != 0 ||
		pod.Spec.Containers[1].Resources.Requests.Cpu().Cmp(resource.MustParse("10m")) != 0 {
		t.Fatalf("small CPU requests = agent %s proxy %s", pod.Spec.Containers[0].Resources.Requests.Cpu(), pod.Spec.Containers[1].Resources.Requests.Cpu())
	}
	if got := tokenVolumeMounts(pod.Spec.Containers[0]); got != 0 {
		t.Fatalf("agent token volume mounts = %d, want 0", got)
	}
	if got := tokenVolumeMounts(pod.Spec.Containers[1]); got != 1 {
		t.Fatalf("proxy token volume mounts = %d, want 1", got)
	}
	assertGatewayTokenProjection(t, pod)
	assertLLMEgressConfiguration(t, pod)
	assertPersistentRuntimeMounts(t, pod)
	if len(pod.Spec.Tolerations) != 2 ||
		pod.Spec.Tolerations[0].TolerationSeconds == nil ||
		*pod.Spec.Tolerations[0].TolerationSeconds != 300 {
		t.Fatalf("tolerations = %#v", pod.Spec.Tolerations)
	}
	requiredAffinity := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	requirement := requiredAffinity.NodeSelectorTerms[0].MatchExpressions[0]
	if requirement.Key != "role" || requirement.Operator != corev1.NodeSelectorOpIn ||
		!reflect.DeepEqual(requirement.Values, []string{"worker"}) {
		t.Fatalf("required node affinity = %#v, want role in [worker]", requirement)
	}
}

func assertAgentSessionPodReconcileIsIdempotent(
	t *testing.T,
	reconciler *AgentSessionReconciler,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) {
	t.Helper()
	version := pod.ResourceVersion
	reconcileAgentSession(t, reconciler, session, 1)
	getObject(t, namespacedName(session.Namespace, pod.Name), pod)
	if pod.ResourceVersion != version {
		t.Fatalf("Pod resourceVersion changed during idempotent reconcile: %s -> %s", version, pod.ResourceVersion)
	}
}

func TestAgentSessionMediumTierUsesFixedBudget(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012407", "018f9c6e-1234-7000-8000-abcdef012507")
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	current.Spec.Tier = bosunv1alpha1.SessionTierMedium
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("set medium tier: %v", err)
	}
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)
	var pvc corev1.PersistentVolumeClaim
	getObject(t, namespacedName(session.Namespace, sessionidentity.PVCName(session.Spec.SessionID)), &pvc)
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Fatalf("medium PVC storage = %s, want 10Gi", storage.String())
	}
	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	if pod.Spec.Containers[0].Resources.Requests.Cpu().Cmp(resource.MustParse("490m")) != 0 ||
		pod.Spec.Containers[0].Resources.Limits.Memory().Cmp(resource.MustParse("1984Mi")) != 0 {
		t.Fatalf("medium agent resources = %#v", pod.Spec.Containers[0].Resources)
	}
}

func TestAgentSessionIdleHibernationRetainsPVCAndResumeReusesIt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012402", "018f9c6e-1234-7000-8000-abcdef012502")
	reconciler := newAgentSessionReconciler()
	reconciler.Now = func() time.Time { return now }
	reconcileAgentSession(t, reconciler, session, 4)

	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := testClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("set test Pod ready: %v", err)
	}

	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	current.Annotations[sessionidentity.LastActiveAnnotation] = now.Add(-31 * time.Minute).Format(time.RFC3339)
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("set last-active annotation: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 6)
	getObject(t, clientKey(session), &current)
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseHibernated {
		t.Fatalf("phase = %q, want Hibernated", current.Status.Phase)
	}
	var pvc corev1.PersistentVolumeClaim
	getObject(t, namespacedName(session.Namespace, sessionidentity.PVCName(session.Spec.SessionID)), &pvc)
	pvcUID := pvc.UID
	if err := testClient.Get(ctx, namespacedName(session.Namespace, pod.Name), &pod); !apierrors.IsNotFound(err) {
		t.Fatalf("hibernated Pod get error = %v, want NotFound", err)
	}

	getObject(t, clientKey(session), &current)
	current.Spec.DesiredState = bosunv1alpha1.DesiredStateRunning
	current.Spec.ResumeNonce = "018f9c6e-1234-7000-8000-abcdef012599"
	current.Annotations[sessionidentity.LastActiveAnnotation] = now.Format(time.RFC3339)
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("request resume: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 4)
	getObject(t, namespacedName(session.Namespace, pvc.Name), &pvc)
	if pvc.UID != pvcUID {
		t.Fatalf("resume replaced PVC: %s -> %s", pvcUID, pvc.UID)
	}
	getObject(t, namespacedName(session.Namespace, pod.Name), &pod)
	if pod.Annotations[sessionidentity.ResumeNonceAnnotation] != current.Spec.ResumeNonce {
		t.Fatalf("resumed Pod nonce = %q", pod.Annotations[sessionidentity.ResumeNonceAnnotation])
	}
}

func TestAgentSessionExplicitHibernateRemainsStable(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012409", "018f9c6e-1234-7000-8000-abcdef012509")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)

	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := testClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("set test Pod ready: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 1)

	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseRunning {
		t.Fatalf("phase before hibernation = %q, want Running", current.Status.Phase)
	}
	current.Spec.DesiredState = bosunv1alpha1.DesiredStateHibernated
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("request hibernation: %v", err)
	}

	reconcileAgentSession(t, reconciler, session, 4)
	for i := range 3 {
		getObject(t, clientKey(session), &current)
		if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseHibernated {
			t.Fatalf("phase after hibernation reconcile #%d = %q, want Hibernated", i+1, current.Status.Phase)
		}
		reconcileAgentSession(t, reconciler, session, 1)
	}
}

func TestAgentSessionDoesNotDeletePodBeforeApplicationRecoveryIsReady(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012410", "018f9c6e-1234-7000-8000-abcdef012510")
	reconciler := newAgentSessionReconciler()
	quiescer := &fakeAgentQuiescer{err: errors.New("drain timeout")}
	reconciler.Quiescer = quiescer
	reconcileAgentSession(t, reconciler, session, 4)

	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := testClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("set test Pod ready: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 1)

	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	current.Spec.DesiredState = bosunv1alpha1.DesiredStateHibernated
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("request hibernation: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 2)

	getObject(t, namespacedName(session.Namespace, pod.Name), &pod)
	getObject(t, clientKey(session), &current)
	if quiescer.calls != 1 {
		t.Fatalf("quiesce calls = %d, want 1", quiescer.calls)
	}
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseHibernating ||
		current.Status.RuntimeCheckpoint == nil ||
		current.Status.RuntimeCheckpoint.State != bosunv1alpha1.RuntimeCheckpointStateCreating {
		t.Fatalf("status after failed quiesce = %#v", current.Status)
	}

	quiescer.err = nil
	reconcileAgentSession(t, reconciler, session, 1)
	getObject(t, namespacedName(session.Namespace, pod.Name), &pod)
	getObject(t, clientKey(session), &current)
	if current.Status.RuntimeCheckpoint == nil ||
		current.Status.RuntimeCheckpoint.State != bosunv1alpha1.RuntimeCheckpointStateReady ||
		current.Status.RuntimeCheckpoint.SHA256 == "" {
		t.Fatalf("ready recovery status = %#v", current.Status.RuntimeCheckpoint)
	}
}

func TestAgentSessionMissingHibernatedPVCFailsWithoutCreatingEmptyWorkspace(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012403", "018f9c6e-1234-7000-8000-abcdef012503")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 2)
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	current.Status.Phase = bosunv1alpha1.AgentSessionPhaseHibernated
	current.Status.PVCName = sessionidentity.PVCName(session.Spec.SessionID)
	current.Status.ObservedResumeNonce = current.Spec.ResumeNonce
	if err := testClient.Status().Update(ctx, &current); err != nil {
		t.Fatalf("mark hibernated: %v", err)
	}
	var pvc corev1.PersistentVolumeClaim
	getObject(t, namespacedName(session.Namespace, current.Status.PVCName), &pvc)
	originalUID := pvc.UID
	if err := testClient.Delete(ctx, &pvc); err != nil {
		t.Fatalf("delete test PVC: %v", err)
	}
	getObject(t, clientKey(session), &current)
	current.Spec.ResumeNonce = "018f9c6e-1234-7000-8000-abcdef012598"
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("request resume: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 1)
	getObject(t, clientKey(session), &current)
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseFailed {
		t.Fatalf("phase = %q, want Failed", current.Status.Phase)
	}
	err := testClient.Get(ctx, namespacedName(session.Namespace, current.Status.PVCName), &pvc)
	if err == nil && (pvc.UID != originalUID || pvc.DeletionTimestamp.IsZero()) {
		t.Fatalf("missing workspace was replaced by a new PVC: uid=%s deletion=%v", pvc.UID, pvc.DeletionTimestamp)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get deleted workspace PVC: %v", err)
	}
}

func TestAgentSessionTransientConflictExhaustsRetryBudget(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012404", "018f9c6e-1234-7000-8000-abcdef012504")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 1) // finalizer
	conflict := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: sessionidentity.PVCName(session.Spec.SessionID), Namespace: session.Namespace,
			Labels: map[string]string{managedByLabel: "someone-else"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			}},
		},
	}
	if err := testClient.Create(ctx, conflict); err != nil {
		t.Fatalf("create conflicting PVC: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, maxTransientRetries)
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseFailed {
		t.Fatalf("phase = %q, want Failed after retry budget", current.Status.Phase)
	}
}

func TestAgentSessionDeadlineConsumesPodNonceWithoutAutomaticRebuild(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012406", "018f9c6e-1234-7000-8000-abcdef012506")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)
	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	pod.Status.Phase = corev1.PodFailed
	pod.Status.Reason = deadlineExceededReason
	if err := testClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("mark Pod deadline exceeded: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 2)
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseHibernated ||
		current.Status.ObservedResumeNonce != session.Spec.ResumeNonce {
		t.Fatalf("deadline status = phase %q nonce %q", current.Status.Phase, current.Status.ObservedResumeNonce)
	}
	reconcileAgentSession(t, reconciler, session, 2)
	if err := testClient.Get(ctx, namespacedName(session.Namespace, pod.Name), &pod); !apierrors.IsNotFound(err) {
		t.Fatalf("consumed nonce rebuilt Pod without explicit resume: %v", err)
	}

	current.Spec.ResumeNonce = "018f9c6e-1234-7000-8000-abcdef012597"
	current.Spec.DesiredState = bosunv1alpha1.DesiredStateRunning
	if err := testClient.Update(ctx, &current); err != nil {
		t.Fatalf("write explicit resume nonce: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 4)
	getObject(t, namespacedName(session.Namespace, pod.Name), &pod)
	if pod.Annotations[sessionidentity.ResumeNonceAnnotation] != current.Spec.ResumeNonce {
		t.Fatalf("resumed Pod nonce = %q, want %q", pod.Annotations[sessionidentity.ResumeNonceAnnotation], current.Spec.ResumeNonce)
	}
}

func TestAgentSessionReportsReadableUnschedulableCapacityCondition(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012408", "018f9c6e-1234-7000-8000-abcdef012508")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)
	var pod corev1.Pod
	getObject(t, namespacedName(session.Namespace, sessionidentity.PodName(session.Spec.SessionID)), &pod)
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason: corev1.PodReasonUnschedulable, Message: "0/4 nodes have sufficient memory",
	}}
	if err := testClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("set Pod unschedulable: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 1)
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	condition := apimeta.FindStatusCondition(current.Status.Conditions, sessionReadyCondition)
	if condition == nil || condition.Reason != "Unschedulable" || condition.Message == "" {
		t.Fatalf("capacity condition = %#v", condition)
	}
}

func TestSupportedPriorityClasses(t *testing.T) {
	for _, name := range []string{lowPriorityClass, normalPriorityClass, highPriorityClass} {
		if !supportedPriorityClass(name) {
			t.Fatalf("supportedPriorityClass(%q) = false", name)
		}
	}
	if supportedPriorityClass("system-cluster-critical") {
		t.Fatal("supportedPriorityClass accepted a platform priority class")
	}
}

func TestAgentSessionFinalizerDeletesPodBeforePVC(t *testing.T) {
	ctx := context.Background()
	session := createAgentSession(t, "018f9c6e-1234-7000-8000-abcdef012405", "018f9c6e-1234-7000-8000-abcdef012505")
	reconciler := newAgentSessionReconciler()
	reconcileAgentSession(t, reconciler, session, 4)
	var current bosunv1alpha1.AgentSession
	getObject(t, clientKey(session), &current)
	if err := testClient.Delete(ctx, &current); err != nil {
		t.Fatalf("delete AgentSession: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 1)
	var pvc corev1.PersistentVolumeClaim
	if err := testClient.Get(ctx, namespacedName(session.Namespace, sessionidentity.PVCName(session.Spec.SessionID)), &pvc); err != nil {
		t.Fatalf("PVC was removed before Pod cleanup completed: %v", err)
	}
	reconcileAgentSession(t, reconciler, session, 6)
	if err := testClient.Get(ctx, clientKey(session), &current); !apierrors.IsNotFound(err) {
		t.Fatalf("AgentSession get after finalization = %v, want NotFound", err)
	}
}

func createAgentSession(t *testing.T, sessionID, userID string) *bosunv1alpha1.AgentSession {
	t.Helper()
	ctx := context.Background()
	if err := testClient.Create(ctx, &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: normalPriorityClass}, Value: 2000,
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create PriorityClass: %v", err)
	}
	namespace := "bosun-u-" + sessionidentity.ShortID(userID)
	if err := testClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	session := &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: sessionidentity.CRName(sessionID), Namespace: namespace,
			Labels:      map[string]string{managedByLabel: managedByValue, userLabel: userID, sessionLabel: sessionID},
			Annotations: map[string]string{sessionidentity.LastActiveAnnotation: time.Now().UTC().Format(time.RFC3339)},
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: sessionID, UserID: userID,
			DesiredState: bosunv1alpha1.DesiredStateRunning, ResumeNonce: sessionID,
			Tier: bosunv1alpha1.SessionTierSmall, Runtime: bosunv1alpha1.RuntimeClaudeCode,
			Provider:      bosunv1alpha1.ProviderSpec{Mode: bosunv1alpha1.ProviderModePlatform},
			StoragePolicy: bosunv1alpha1.StoragePolicyLocal, IdleTimeoutSeconds: 1800,
			ActiveDeadlineSeconds: 28800, PriorityClassName: normalPriorityClass,
		},
	}
	if err := testClient.Create(ctx, session); err != nil {
		t.Fatalf("create AgentSession: %v", err)
	}
	return session
}

func newAgentSessionReconciler() *AgentSessionReconciler {
	return &AgentSessionReconciler{
		Client: testClient, Scheme: testScheme,
		AgentImage:       "registry.example/agent:1234567",
		AgentPullPolicy:  corev1.PullAlways,
		StorageClassName: "local-path", GatewayURL: "http://bosun-gateway:8081",
		EgressProxyURL: "http://bosun-egress-proxy:3128", IdleScanInterval: time.Millisecond,
		Quiescer: &fakeAgentQuiescer{}, StateReader: &fakeAgentStateReader{},
	}
}

type fakeAgentQuiescer struct {
	err   error
	calls int
}

type fakeAgentStateReader struct {
	state AgentWorkState
	err   error
}

func (r *fakeAgentStateReader) ReadState(_ context.Context, _ *corev1.Pod) (AgentWorkState, error) {
	return r.state, r.err
}

func (q *fakeAgentQuiescer) Quiesce(_ context.Context, pod *corev1.Pod) (QuiesceResult, error) {
	q.calls++
	if q.err != nil {
		return QuiesceResult{}, q.err
	}
	return QuiesceResult{
		CreatedAt: time.Now().UTC(), SizeBytes: 128, SHA256: strings.Repeat("a", 64),
		AgentImageDigest: podAgentImageDigest(pod),
	}, nil
}

func reconcileAgentSession(t *testing.T, reconciler *AgentSessionReconciler, session *bosunv1alpha1.AgentSession, count int) {
	t.Helper()
	request := ctrl.Request{NamespacedName: clientKey(session)}
	for i := range count {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatalf("Reconcile() #%d error = %v", i+1, err)
		}
	}
}

func clientKey(session *bosunv1alpha1.AgentSession) types.NamespacedName {
	return types.NamespacedName{Name: session.Name, Namespace: session.Namespace}
}

func assertSessionLabels(t *testing.T, labels map[string]string, session *bosunv1alpha1.AgentSession) {
	t.Helper()
	if labels[managedByLabel] != managedByValue || labels[userLabel] != session.Spec.UserID || labels[sessionLabel] != session.Spec.SessionID {
		t.Fatalf("labels = %#v", labels)
	}
}

func assertRestrictedContainer(t *testing.T, container corev1.Container) {
	t.Helper()
	security := container.SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
		security.Capabilities == nil || len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("%s securityContext = %#v", container.Name, security)
	}
	if len(container.Resources.Requests) == 0 || len(container.Resources.Limits) == 0 {
		t.Fatalf("%s missing resources", container.Name)
	}
}

func tokenVolumeMounts(container corev1.Container) int {
	count := 0
	for _, mount := range container.VolumeMounts {
		if mount.Name == gatewayTokenVolume {
			count++
		}
	}
	return count
}

func assertGatewayTokenProjection(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != gatewayTokenVolume {
			continue
		}
		if volume.Projected == nil || len(volume.Projected.Sources) != 1 ||
			volume.Projected.Sources[0].ServiceAccountToken == nil {
			t.Fatalf("gateway token volume = %#v", volume)
		}
		projection := volume.Projected.Sources[0].ServiceAccountToken
		if projection.Audience != "bosun-llm-gateway" ||
			projection.ExpirationSeconds == nil ||
			*projection.ExpirationSeconds != 3600 ||
			projection.Path != "token" {
			t.Fatalf("gateway token projection = %#v", projection)
		}
		return
	}
	t.Fatal("gateway token projected volume is missing")
}

func assertLLMEgressConfiguration(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	agentEnv := make(map[string]string)
	for _, variable := range pod.Spec.Containers[0].Env {
		agentEnv[variable.Name] = variable.Value
	}
	if agentEnv["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8080" ||
		agentEnv["ANTHROPIC_API_KEY"] != "sk-xxxx" ||
		agentEnv["HTTPS_PROXY"] == "" ||
		!strings.Contains(agentEnv["NO_PROXY"], "127.0.0.1") {
		t.Fatalf("agent LLM egress env = %#v", agentEnv)
	}
	proxy := pod.Spec.Containers[1]
	if len(proxy.Args) != 3 ||
		proxy.Args[0] != "--listen=127.0.0.1:8080" ||
		proxy.ReadinessProbe == nil ||
		proxy.ReadinessProbe.Exec == nil ||
		len(proxy.ReadinessProbe.Exec.Command) != 2 {
		t.Fatalf("auth proxy configuration = %#v", proxy)
	}
}

func assertPersistentRuntimeMounts(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	mounts := make(map[string]corev1.VolumeMount)
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		mounts[mount.MountPath] = mount
	}
	if mounts["/workspace"].Name != workspaceVolume ||
		mounts["/tmp"].SubPath != ".bosun-state/runtime/tmp" ||
		mounts["/run/bosun-tmux"].SubPath != ".bosun-state/runtime/tmux" {
		t.Fatalf("agent runtime mounts = %#v", mounts)
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "tmp" || volume.Name == "tmux" {
			t.Fatalf("agent runtime volume %q must use the workspace PVC", volume.Name)
		}
	}
}
