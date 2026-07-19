/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const (
	agentSessionFinalizer  = "bosun.io/agentsession-cleanup"
	sessionLabel           = "bosun.io/session"
	sessionReadyCondition  = "Ready"
	sessionRetryCondition  = "ReconcileRetry"
	gatewayTokenVolume     = "gateway-token"
	freePriorityClass      = "bosun-free"
	deadlineExceededReason = "DeadlineExceeded"
	maxTransientRetries    = 10
	defaultIdleScan        = 30 * time.Second
	maxHibernateGrace      = int64(30)
	reconcileTimeout       = 30 * time.Second
)

// AgentSessionReconciler reconciles AgentSession workload and lifecycle state.
type AgentSessionReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	AgentImage       string
	StorageClassName string
	GatewayURL       string
	EgressProxyURL   string
	IdleScanInterval time.Duration
	Now              func() time.Time
}

// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions,verbs=get;list;watch
// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=serviceaccounts;pods;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one AgentSession toward its desired state without modifying spec.
func (r *AgentSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	log := logf.FromContext(ctx)
	var session bosunv1alpha1.AgentSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get AgentSession %s: %w", req.NamespacedName, err)
	}

	if !session.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &session)
	}
	if err := validateAgentSession(&session); err != nil {
		if statusErr := r.setStatus(ctx, req.NamespacedName, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = bosunv1alpha1.AgentSessionPhaseFailed
			setSessionCondition(status, session.Generation, metav1.ConditionFalse, "InvalidSpec", err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		log.Info("Rejected AgentSession with invalid spec", "name", session.Name, "reason", err)
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&session, agentSessionFinalizer) {
		controllerutil.AddFinalizer(&session, agentSessionFinalizer)
		if err := r.Update(ctx, &session); err != nil {
			return ctrl.Result{}, fmt.Errorf("add AgentSession finalizer %s: %w", session.Name, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch {
	case session.Spec.DesiredState == bosunv1alpha1.DesiredStateHibernated:
		return r.reconcileHibernate(ctx, &session)
	case session.Status.Phase == bosunv1alpha1.AgentSessionPhaseHibernating:
		// Finish deleting the old Pod before consuming a resume received mid-hibernation.
		return r.reconcileHibernate(ctx, &session)
	case session.Status.Phase == bosunv1alpha1.AgentSessionPhaseHibernated &&
		session.Status.ObservedResumeNonce == session.Spec.ResumeNonce:
		return ctrl.Result{}, nil
	case session.Status.Phase == bosunv1alpha1.AgentSessionPhaseFailed &&
		session.Status.ObservedGeneration == session.Generation &&
		session.Status.ObservedResumeNonce == session.Spec.ResumeNonce:
		return ctrl.Result{}, nil
	}
	return r.reconcileRunning(ctx, &session)
}

//nolint:gocyclo // The branches are the explicit AgentSession state machine transitions.
func (r *AgentSessionReconciler) reconcileRunning(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(session)
	pvc, pvcExists, err := r.getPVC(ctx, session)
	if err != nil {
		return r.handleTransient(ctx, session, err)
	}
	restoring := session.Status.Phase == bosunv1alpha1.AgentSessionPhaseHibernated ||
		session.Status.Phase == bosunv1alpha1.AgentSessionPhaseRestoring
	if restoring && session.Status.PVCName != "" && !pvcExists {
		return r.fail(ctx, session, "WorkspaceUnavailable", "Original workspace PVC is unavailable; an empty workspace was not created")
	}
	if !pvcExists {
		pvc = desiredPVC(session, r.StorageClassName)
		if err := controllerutil.SetControllerReference(session, pvc, r.Scheme); err != nil {
			return r.fail(ctx, session, "OwnershipError", fmt.Sprintf("Could not own workspace PVC: %v", err))
		}
		if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
			return r.handleTransient(ctx, session, fmt.Errorf("create PersistentVolumeClaim %s: %w", pvc.Name, err))
		}
	}
	if err := r.ensureServiceAccount(ctx, session); err != nil {
		return r.handleTransient(ctx, session, err)
	}

	phase := bosunv1alpha1.AgentSessionPhaseProvisioning
	if restoring {
		phase = bosunv1alpha1.AgentSessionPhaseRestoring
	}
	if session.Status.Phase == "" || session.Status.Phase == bosunv1alpha1.AgentSessionPhasePending ||
		session.Status.Phase == bosunv1alpha1.AgentSessionPhaseHibernated ||
		session.Status.Phase == bosunv1alpha1.AgentSessionPhaseFailed {
		if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = phase
			status.PVCName = pvc.Name
			status.ObservedGeneration = session.Generation
			setSessionCondition(status, session.Generation, metav1.ConditionFalse, string(phase), "Agent workload is being prepared")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	pod, exists, err := r.getPod(ctx, session)
	if err != nil {
		return r.handleTransient(ctx, session, err)
	}
	if !exists {
		pod = r.desiredPod(session, pvc.Name)
		if err := controllerutil.SetControllerReference(session, pod, r.Scheme); err != nil {
			return r.fail(ctx, session, "OwnershipError", fmt.Sprintf("Could not own agent Pod: %v", err))
		}
		if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
			return r.handleTransient(ctx, session, fmt.Errorf("create Pod %s: %w", pod.Name, err))
		}
		if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = phase
			status.PodName = pod.Name
			status.PVCName = pvc.Name
			status.ObservedGeneration = session.Generation
			setSessionCondition(status, session.Generation, metav1.ConditionFalse, string(phase), "Agent Pod is starting")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.idleScan()}, nil
	}

	if podStoppedByDeadline(pod) {
		return r.hibernateStoppedPod(ctx, session, pod)
	}
	if pod.Status.Phase == corev1.PodFailed {
		return r.handleTransient(ctx, session, fmt.Errorf("agent Pod failed: %s", pod.Status.Message))
	}
	if condition := podScheduledCondition(pod); condition != nil &&
		condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
		if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = phase
			status.PodName = pod.Name
			status.PVCName = pvc.Name
			status.ObservedGeneration = session.Generation
			setSessionCondition(status, session.Generation, metav1.ConditionFalse, "Unschedulable", readableSchedulingMessage(condition.Message))
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.idleScan()}, nil
	}
	if !podReady(pod) {
		return ctrl.Result{RequeueAfter: r.idleScan()}, nil
	}

	activity := sessionActivity(session, pod.CreationTimestamp.Time)
	now := r.now()
	idle := now.Sub(activity) >= time.Duration(session.Spec.IdleTimeoutSeconds)*time.Second
	if idle && session.Status.Phase == bosunv1alpha1.AgentSessionPhaseRunning {
		if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = bosunv1alpha1.AgentSessionPhaseIdle
			status.LastActiveAt = &metav1.Time{Time: activity}
			status.NodeName = pod.Spec.NodeName
			status.PodName = pod.Name
			status.PVCName = pvc.Name
			status.ObservedGeneration = session.Generation
			status.ObservedResumeNonce = pod.Annotations[sessionidentity.ResumeNonceAnnotation]
			setSessionCondition(status, session.Generation, metav1.ConditionTrue, "SessionIdle", "Agent Pod is idle and awaiting confirmation before hibernation")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.idleScan()}, nil
	}
	if idle && session.Status.Phase == bosunv1alpha1.AgentSessionPhaseIdle {
		return r.reconcileHibernate(ctx, session)
	}

	if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		status.Phase = bosunv1alpha1.AgentSessionPhaseRunning
		status.LastActiveAt = &metav1.Time{Time: activity}
		status.NodeName = pod.Spec.NodeName
		status.PodName = pod.Name
		status.PVCName = pvc.Name
		status.ObservedGeneration = session.Generation
		status.ObservedResumeNonce = pod.Annotations[sessionidentity.ResumeNonceAnnotation]
		clearRetryCondition(status)
		setSessionCondition(status, session.Generation, metav1.ConditionTrue, "SessionRunning", "Agent Pod and persistent terminal are ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.idleScan()}, nil
}

func (r *AgentSessionReconciler) reconcileHibernate(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(session)
	if session.Status.Phase != bosunv1alpha1.AgentSessionPhaseHibernating {
		if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
			status.Phase = bosunv1alpha1.AgentSessionPhaseHibernating
			status.ObservedGeneration = session.Generation
			setSessionCondition(status, session.Generation, metav1.ConditionFalse, "Hibernating", "Agent Pod is stopping; workspace PVC is retained")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	pod, exists, err := r.getPod(ctx, session)
	if err != nil {
		return r.handleTransient(ctx, session, err)
	}
	if exists {
		grace := maxHibernateGrace
		if err := r.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
			return r.handleTransient(ctx, session, fmt.Errorf("delete Pod %s during hibernation: %w", pod.Name, err))
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err := r.setStatus(ctx, key, session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		status.Phase = bosunv1alpha1.AgentSessionPhaseHibernated
		status.PodName = ""
		status.NodeName = ""
		status.ObservedGeneration = session.Generation
		// Explicit hibernate consumes the current nonce. A resume received while
		// Hibernating has a new nonce and remains unconsumed.
		if session.Spec.DesiredState == bosunv1alpha1.DesiredStateHibernated {
			status.ObservedResumeNonce = session.Spec.ResumeNonce
		}
		setSessionCondition(status, session.Generation, metav1.ConditionFalse, "SessionHibernated", "Agent Pod is stopped and workspace PVC is retained")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: session.Spec.DesiredState == bosunv1alpha1.DesiredStateRunning}, nil
}

func (r *AgentSessionReconciler) hibernateStoppedPod(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) (ctrl.Result, error) {
	nonce := pod.Annotations[sessionidentity.ResumeNonceAnnotation]
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return r.handleTransient(ctx, session, fmt.Errorf("delete deadline-stopped Pod %s: %w", pod.Name, err))
	}
	if err := r.setStatus(ctx, client.ObjectKeyFromObject(session), session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		status.Phase = bosunv1alpha1.AgentSessionPhaseHibernated
		status.PodName = ""
		status.NodeName = ""
		status.ObservedGeneration = session.Generation
		status.ObservedResumeNonce = nonce
		setSessionCondition(status, session.Generation, metav1.ConditionFalse, "ActiveDeadlineReached", "Agent Pod reached its active deadline; workspace PVC is retained")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: nonce != session.Spec.ResumeNonce}, nil
}

func (r *AgentSessionReconciler) reconcileDelete(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(session, agentSessionFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.setStatus(ctx, client.ObjectKeyFromObject(session), session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		status.Phase = bosunv1alpha1.AgentSessionPhaseDeleting
		status.ObservedGeneration = session.Generation
		setSessionCondition(status, session.Generation, metav1.ConditionFalse, "Deleting", "Permanently deleting agent Pod and workspace PVC")
	}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	pod, exists, err := r.getPod(ctx, session)
	if err != nil {
		return r.handleTransient(ctx, session, err)
	}
	if exists {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return r.handleTransient(ctx, session, fmt.Errorf("delete Pod %s: %w", pod.Name, err))
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: sessionidentity.ServiceAccountName(session.Spec.SessionID), Namespace: session.Namespace,
	}}
	if err := r.Delete(ctx, sa); err != nil && !apierrors.IsNotFound(err) {
		return r.handleTransient(ctx, session, fmt.Errorf("delete ServiceAccount %s: %w", sa.Name, err))
	}
	pvc, exists, err := r.getPVC(ctx, session)
	if err != nil {
		return r.handleTransient(ctx, session, err)
	}
	if exists {
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return r.handleTransient(ctx, session, fmt.Errorf("delete PersistentVolumeClaim %s: %w", pvc.Name, err))
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	controllerutil.RemoveFinalizer(session, agentSessionFinalizer)
	if err := r.Update(ctx, session); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("remove AgentSession finalizer %s: %w", session.Name, err)
	}
	return ctrl.Result{}, nil
}

func (r *AgentSessionReconciler) ensureServiceAccount(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) error {
	key := types.NamespacedName{
		Name: sessionidentity.ServiceAccountName(session.Spec.SessionID), Namespace: session.Namespace,
	}
	var existing corev1.ServiceAccount
	if err := r.Get(ctx, key, &existing); err == nil {
		return validateSessionWorkload(&existing, session)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get ServiceAccount %s: %w", key, err)
	}
	automount := false
	sa := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: sessionLabels(session)},
		AutomountServiceAccountToken: &automount,
	}
	if err := controllerutil.SetControllerReference(session, sa, r.Scheme); err != nil {
		return fmt.Errorf("own ServiceAccount %s: %w", key, err)
	}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ServiceAccount %s: %w", key, err)
	}
	return nil
}

func (r *AgentSessionReconciler) getPod(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) (*corev1.Pod, bool, error) {
	var pod corev1.Pod
	key := types.NamespacedName{Name: sessionidentity.PodName(session.Spec.SessionID), Namespace: session.Namespace}
	if err := r.Get(ctx, key, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return &pod, false, nil
		}
		return nil, false, fmt.Errorf("get Pod %s: %w", key, err)
	}
	if err := validateSessionWorkload(&pod, session); err != nil {
		return nil, false, err
	}
	return &pod, true, nil
}

func (r *AgentSessionReconciler) getPVC(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) (*corev1.PersistentVolumeClaim, bool, error) {
	var pvc corev1.PersistentVolumeClaim
	key := types.NamespacedName{Name: sessionidentity.PVCName(session.Spec.SessionID), Namespace: session.Namespace}
	if err := r.Get(ctx, key, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return &pvc, false, nil
		}
		return nil, false, fmt.Errorf("get PersistentVolumeClaim %s: %w", key, err)
	}
	if !pvc.DeletionTimestamp.IsZero() {
		return &pvc, false, nil
	}
	if err := validateSessionWorkload(&pvc, session); err != nil {
		return nil, false, err
	}
	return &pvc, true, nil
}

func desiredPVC(session *bosunv1alpha1.AgentSession, storageClass string) *corev1.PersistentVolumeClaim {
	size := "5Gi"
	if session.Spec.Tier == bosunv1alpha1.SessionTierMedium {
		size = "10Gi"
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: sessionidentity.PVCName(session.Spec.SessionID), Namespace: session.Namespace,
			Labels: sessionLabels(session),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
			StorageClassName: &storageClass,
		},
	}
}

func (r *AgentSessionReconciler) desiredPod(
	session *bosunv1alpha1.AgentSession,
	pvcName string,
) *corev1.Pod {
	agentRequests, agentLimits := tierAgentResources(session.Spec.Tier)
	runAsUser := int64(10001)
	runAsGroup := int64(10001)
	nonRoot := true
	noPrivilege := false
	readOnly := true
	fsGroupChangePolicy := corev1.FSGroupChangeOnRootMismatch
	tokenMode := int32(0o440)
	expiration := int64(3600)
	tcp := corev1.ProtocolTCP
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionidentity.PodName(session.Spec.SessionID),
			Namespace: session.Namespace,
			Labels:    sessionLabels(session),
			Annotations: map[string]string{
				sessionidentity.ResumeNonceAnnotation: session.Spec.ResumeNonce,
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:            sessionidentity.ServiceAccountName(session.Spec.SessionID),
			AutomountServiceAccountToken:  &noPrivilege,
			RestartPolicy:                 corev1.RestartPolicyNever,
			PriorityClassName:             session.Spec.PriorityClassName,
			ActiveDeadlineSeconds:         &session.Spec.ActiveDeadlineSeconds,
			TerminationGracePeriodSeconds: ptrInt64(maxHibernateGrace),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:        &nonRoot,
				RunAsUser:           &runAsUser,
				RunAsGroup:          &runAsGroup,
				FSGroup:             &runAsGroup,
				FSGroupChangePolicy: &fsGroupChangePolicy,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Affinity:    agentAffinity(),
			Tolerations: agentTolerations(),
			Containers: []corev1.Container{
				{
					Name:  "agent",
					Image: r.AgentImage,
					Env: []corev1.EnvVar{
						{Name: "ANTHROPIC_BASE_URL", Value: "http://127.0.0.1:8080"},
						{Name: "ANTHROPIC_API_KEY", Value: "sk-xxxx"},
						{Name: "HTTPS_PROXY", Value: r.EgressProxyURL},
						{Name: "NO_PROXY", Value: "127.0.0.1,localhost"},
					},
					Resources:       corev1.ResourceRequirements{Requests: agentRequests, Limits: agentLimits},
					SecurityContext: restrictedContainerSecurityContext(&noPrivilege, &readOnly),
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "tmp", MountPath: "/tmp"},
						{Name: "tmux", MountPath: "/run/bosun-tmux"},
					},
					ReadinessProbe: execProbe("tmux", "has-session", "-t", "bosun"),
					LivenessProbe:  execProbe("tmux", "has-session", "-t", "bosun"),
				},
				{
					Name:    "auth-proxy",
					Image:   r.AgentImage,
					Command: []string{"/usr/local/bin/bosun-auth-proxy"},
					Args:    []string{"--listen=127.0.0.1:8080", "--upstream=" + r.GatewayURL, "--token-file=/var/run/secrets/bosun/token"},
					Ports:   []corev1.ContainerPort{{Name: "proxy", ContainerPort: 8080, Protocol: tcp}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
					},
					SecurityContext: restrictedContainerSecurityContext(&noPrivilege, &readOnly),
					VolumeMounts: []corev1.VolumeMount{
						{Name: gatewayTokenVolume, MountPath: "/var/run/secrets/bosun", ReadOnly: true},
						{Name: "proxy-tmp", MountPath: "/tmp"},
					},
					ReadinessProbe: execProbe(
						"/usr/local/bin/bosun-auth-proxy",
						"--healthcheck=http://127.0.0.1:8080/healthz",
					),
					LivenessProbe: execProbe(
						"/usr/local/bin/bosun-auth-proxy",
						"--healthcheck=http://127.0.0.1:8080/healthz",
					),
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					}},
				},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPtr("1Gi")}}},
				{Name: "tmux", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPtr("16Mi")}}},
				{Name: "proxy-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPtr("16Mi")}}},
				{
					Name: gatewayTokenVolume,
					VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
						DefaultMode: &tokenMode,
						Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Audience: "bosun-llm-gateway", ExpirationSeconds: &expiration, Path: "token",
						}}},
					}},
				},
			},
		},
	}
}

func agentAffinity() *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "role", Operator: corev1.NodeSelectorOpIn, Values: []string{"core", "worker"},
			}}}},
		},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
			{Weight: 100, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "region", Operator: corev1.NodeSelectorOpIn, Values: []string{"sg"},
			}}}},
			{Weight: 50, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "region", Operator: corev1.NodeSelectorOpIn, Values: []string{"cn"},
			}}}},
		},
	}}
}

func agentTolerations() []corev1.Toleration {
	return []corev1.Toleration{
		{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptrInt64(300)},
		{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptrInt64(300)},
	}
}

func tierAgentResources(tier bosunv1alpha1.SessionTier) (corev1.ResourceList, corev1.ResourceList) {
	if tier == bosunv1alpha1.SessionTierMedium {
		return corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("490m"), corev1.ResourceMemory: resource.MustParse("1008Mi"),
			}, corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("950m"), corev1.ResourceMemory: resource.MustParse("1984Mi"),
			}
	}
	return corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("240m"), corev1.ResourceMemory: resource.MustParse("496Mi"),
		}, corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("450m"), corev1.ResourceMemory: resource.MustParse("960Mi"),
		}
}

func restrictedContainerSecurityContext(noPrivilege, readOnly *bool) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: noPrivilege,
		ReadOnlyRootFilesystem:   readOnly,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func execProbe(command ...string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
		InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3,
	}
}

func (r *AgentSessionReconciler) handleTransient(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	cause error,
) (ctrl.Result, error) {
	attempt := retryAttempt(session.Status.Conditions, session.Generation) + 1
	if attempt >= maxTransientRetries {
		return r.fail(ctx, session, "RetryBudgetExceeded", fmt.Sprintf("Reconciliation failed after %d attempts", attempt))
	}
	if err := r.setStatus(ctx, client.ObjectKeyFromObject(session), session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		if status.Phase == "" {
			status.Phase = bosunv1alpha1.AgentSessionPhasePending
		}
		status.ObservedGeneration = session.Generation
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: sessionRetryCondition, Status: metav1.ConditionTrue, ObservedGeneration: session.Generation,
			Reason: "TransientError", Message: fmt.Sprintf("attempt=%d; %s", attempt, safeConditionMessage(cause.Error())),
		})
	}); err != nil {
		return ctrl.Result{}, errors.Join(cause, err)
	}
	delay := time.Second << min(attempt-1, 8)
	return ctrl.Result{RequeueAfter: delay}, nil
}

func (r *AgentSessionReconciler) fail(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	reason string,
	message string,
) (ctrl.Result, error) {
	err := r.setStatus(ctx, client.ObjectKeyFromObject(session), session.Generation, func(status *bosunv1alpha1.AgentSessionStatus) {
		status.Phase = bosunv1alpha1.AgentSessionPhaseFailed
		status.ObservedGeneration = session.Generation
		status.ObservedResumeNonce = session.Spec.ResumeNonce
		setSessionCondition(status, session.Generation, metav1.ConditionFalse, reason, message)
	})
	return ctrl.Result{}, err
}

func (r *AgentSessionReconciler) setStatus(
	ctx context.Context,
	key types.NamespacedName,
	expectedGeneration int64,
	mutate func(*bosunv1alpha1.AgentSessionStatus),
) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current bosunv1alpha1.AgentSession
		if err := r.Get(ctx, key, &current); err != nil {
			return fmt.Errorf("refresh AgentSession %s before status update: %w", key, err)
		}
		if current.Generation != expectedGeneration {
			return fmt.Errorf("AgentSession generation changed from %d to %d", expectedGeneration, current.Generation)
		}
		desired := current.Status.DeepCopy()
		mutate(desired)
		if reflect.DeepEqual(current.Status, *desired) {
			return nil
		}
		current.Status = *desired
		if err := r.Status().Update(ctx, &current); err != nil {
			return fmt.Errorf("update AgentSession %s status: %w", key, err)
		}
		return nil
	})
}

func setSessionCondition(
	status *bosunv1alpha1.AgentSessionStatus,
	generation int64,
	conditionStatus metav1.ConditionStatus,
	reason string,
	message string,
) {
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: sessionReadyCondition, Status: conditionStatus, ObservedGeneration: generation,
		Reason: reason, Message: safeConditionMessage(message),
	})
}

func clearRetryCondition(status *bosunv1alpha1.AgentSessionStatus) {
	apimeta.RemoveStatusCondition(&status.Conditions, sessionRetryCondition)
}

func retryAttempt(conditions []metav1.Condition, generation int64) int {
	condition := apimeta.FindStatusCondition(conditions, sessionRetryCondition)
	if condition == nil || condition.ObservedGeneration != generation {
		return 0
	}
	prefix := "attempt="
	if !strings.HasPrefix(condition.Message, prefix) {
		return 0
	}
	value := strings.SplitN(strings.TrimPrefix(condition.Message, prefix), ";", 2)[0]
	attempt, _ := strconv.Atoi(value)
	return attempt
}

func validateAgentSession(session *bosunv1alpha1.AgentSession) error {
	if !userIDPattern.MatchString(session.Spec.SessionID) || !userIDPattern.MatchString(session.Spec.UserID) {
		return fmt.Errorf("spec.sessionID and spec.userID must be UUID v7")
	}
	if session.Name != sessionidentity.CRName(session.Spec.SessionID) {
		return fmt.Errorf("metadata.name must match spec.sessionID")
	}
	if session.Labels[managedByLabel] != managedByValue ||
		session.Labels[userLabel] != session.Spec.UserID ||
		session.Labels[sessionLabel] != session.Spec.SessionID {
		return fmt.Errorf("required Bosun management labels must match spec IDs")
	}
	if session.Spec.Runtime != bosunv1alpha1.RuntimeClaudeCode ||
		session.Spec.Provider.Mode != bosunv1alpha1.ProviderModePlatform ||
		session.Spec.Provider.CredentialID != "" ||
		session.Spec.StoragePolicy != bosunv1alpha1.StoragePolicyLocal ||
		session.Spec.PriorityClassName != freePriorityClass {
		return fmt.Errorf("P0 only supports platform + local + claude-code + bosun-free")
	}
	return nil
}

func validateSessionWorkload(object client.Object, session *bosunv1alpha1.AgentSession) error {
	labels := object.GetLabels()
	if labels[managedByLabel] != managedByValue ||
		labels[userLabel] != session.Spec.UserID ||
		labels[sessionLabel] != session.Spec.SessionID {
		return fmt.Errorf("%T %s is not managed by Bosun for this session", object, client.ObjectKeyFromObject(object))
	}
	return nil
}

func sessionLabels(session *bosunv1alpha1.AgentSession) map[string]string {
	return map[string]string{
		managedByLabel: managedByValue,
		userLabel:      session.Spec.UserID,
		sessionLabel:   session.Spec.SessionID,
	}
}

func podReady(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return pod.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

func podScheduledCondition(pod *corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodScheduled {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func podStoppedByDeadline(pod *corev1.Pod) bool {
	if pod.Status.Reason == deadlineExceededReason {
		return true
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated != nil && status.State.Terminated.Reason == deadlineExceededReason {
			return true
		}
	}
	return false
}

func sessionActivity(session *bosunv1alpha1.AgentSession, fallback time.Time) time.Time {
	if raw := session.Annotations[sessionidentity.LastActiveAnnotation]; raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC()
		}
	}
	if session.Status.LastActiveAt != nil {
		return session.Status.LastActiveAt.UTC()
	}
	return fallback.UTC()
}

func safeConditionMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func readableSchedulingMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "No eligible node currently has sufficient capacity"
	}
	return safeConditionMessage(message)
}

func quantityPtr(raw string) *resource.Quantity {
	value := resource.MustParse(raw)
	return &value
}

func ptrInt64(value int64) *int64 {
	return &value
}

func (r *AgentSessionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *AgentSessionReconciler) idleScan() time.Duration {
	if r.IdleScanInterval > 0 {
		return r.IdleScanInterval
	}
	return defaultIdleScan
}

// SetupWithManager sets up watches for the CR and all owned workload resources.
func (r *AgentSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	switch {
	case r.AgentImage == "":
		return fmt.Errorf("agent image must not be empty")
	case r.StorageClassName == "":
		return fmt.Errorf("storage class name must not be empty")
	case r.GatewayURL == "":
		return fmt.Errorf("gateway URL must not be empty")
	case r.EgressProxyURL == "":
		return fmt.Errorf("egress proxy URL must not be empty")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&bosunv1alpha1.AgentSession{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.Pod{}).
		Named("agentsession").
		Complete(r)
}
