package terminal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"

	"github.com/Amsors/Bosun/backend/internal/session"
	"github.com/Amsors/Bosun/backend/internal/userenv"
)

func TestKubernetesRuntimeAuthorizeChecksNamespaceCRPhaseAndPodLabels(t *testing.T) {
	record := terminalSessionRecord(t)
	cr := terminalAgentSession(record)
	pod := terminalPod(record)
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	kubeRuntime := &KubernetesRuntime{
		core: kubernetesfake.NewSimpleClientset(pod),
		crs:  clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build(),
	}

	target, err := kubeRuntime.Authorize(context.Background(), record)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if target.PodName != pod.Name || target.Namespace != pod.Namespace {
		t.Fatalf("Authorize() target = %#v", target)
	}

	pod.Labels["bosun.io/session"] = "another-session"
	kubeRuntime.core = kubernetesfake.NewSimpleClientset(pod)
	if _, err := kubeRuntime.Authorize(context.Background(), record); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Authorize() label mismatch error = %v", err)
	}

	cr.Status.Phase = bosunv1alpha1.AgentSessionPhaseHibernating
	kubeRuntime.crs = clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()
	if _, err := kubeRuntime.Authorize(context.Background(), record); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Authorize() hibernating error = %v", err)
	}
}

func TestKubernetesRuntimeUpdateActivityHonorsPersistedInterval(t *testing.T) {
	record := terminalSessionRecord(t)
	cr := terminalAgentSession(record)
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	crClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()
	kubeRuntime := &KubernetesRuntime{crs: crClient}
	target := Target{
		SessionID: record.ID, UserID: record.UserID,
		Namespace: record.CRNamespace, CRName: record.CRName,
	}
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if err := kubeRuntime.UpdateActivity(context.Background(), target, start, 15*time.Second); err != nil {
		t.Fatalf("first UpdateActivity() error = %v", err)
	}
	if err := kubeRuntime.UpdateActivity(context.Background(), target, start.Add(time.Second), 15*time.Second); err != nil {
		t.Fatalf("throttled UpdateActivity() error = %v", err)
	}
	var current bosunv1alpha1.AgentSession
	if err := crClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: record.CRNamespace, Name: record.CRName},
		&current,
	); err != nil {
		t.Fatalf("get updated AgentSession: %v", err)
	}
	if got := current.Annotations[sessionidentity.LastActiveAnnotation]; got != start.Format(time.RFC3339) {
		t.Fatalf("last-active annotation = %q, want %q", got, start.Format(time.RFC3339))
	}
}

func terminalSessionRecord(t *testing.T) session.Session {
	t.Helper()
	sessionID := "018f9c6e-1234-7000-8000-abcdef012411"
	userID := "018f9c6e-1234-7000-8000-abcdef012511"
	record := session.Session{}
	var err error
	record.ID, err = uuid.Parse(sessionID)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	record.UserID, err = uuid.Parse(userID)
	if err != nil {
		t.Fatalf("parse user ID: %v", err)
	}
	record.CRNamespace = userenv.Namespace(userID)
	record.CRName = sessionidentity.CRName(sessionID)
	record.Phase = "Running"
	return record
}

func terminalAgentSession(record session.Session) *bosunv1alpha1.AgentSession {
	return &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: record.CRName, Namespace: record.CRNamespace},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: record.ID.String(), UserID: record.UserID.String(),
		},
		Status: bosunv1alpha1.AgentSessionStatus{
			Phase:   bosunv1alpha1.AgentSessionPhaseRunning,
			PodName: sessionidentity.PodName(record.ID.String()),
		},
	}
}

func terminalPod(record session.Session) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: sessionidentity.PodName(record.ID.String()), Namespace: record.CRNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "bosun",
				"bosun.io/user":                record.UserID.String(),
				"bosun.io/session":             record.ID.String(),
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: agentContainerName}}},
	}
}
