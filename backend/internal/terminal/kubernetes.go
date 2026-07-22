package terminal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"

	"github.com/Amsors/Bosun/backend/internal/session"
	"github.com/Amsors/Bosun/backend/internal/userenv"
)

// KubernetesRuntime owns clients created solely for terminal authorization and
// pods/exec. It is deliberately not shared with ordinary REST handlers because
// pods/exec RBAC cannot be constrained by label selectors.
type KubernetesRuntime struct {
	config *rest.Config
	core   kubernetes.Interface
	crs    client.Client
}

func NewKubernetesRuntime(restConfig *rest.Config) (*KubernetesRuntime, error) {
	if restConfig == nil {
		return nil, fmt.Errorf("terminal Kubernetes rest config is required")
	}
	config := rest.CopyConfig(restConfig)
	coreClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create terminal core Kubernetes client: %w", err)
	}
	crScheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(crScheme); err != nil {
		return nil, fmt.Errorf("register terminal AgentSession scheme: %w", err)
	}
	crClient, err := client.New(rest.CopyConfig(restConfig), client.Options{Scheme: crScheme})
	if err != nil {
		return nil, fmt.Errorf("create terminal CR Kubernetes client: %w", err)
	}
	return &KubernetesRuntime{config: config, core: coreClient, crs: crClient}, nil
}

func (k *KubernetesRuntime) Authorize(ctx context.Context, record session.Session) (Target, error) {
	expectedNamespace := userenv.Namespace(record.UserID.String())
	if record.CRNamespace != expectedNamespace || record.CRName != sessionidentity.CRName(record.ID.String()) {
		return Target{}, ErrForbidden
	}

	callCtx, cancel := context.WithTimeout(ctx, kubernetesCallTimeout)
	defer cancel()
	var cr bosunv1alpha1.AgentSession
	key := types.NamespacedName{Namespace: record.CRNamespace, Name: record.CRName}
	if err := k.crs.Get(callCtx, key, &cr); err != nil {
		return Target{}, fmt.Errorf("get terminal AgentSession %s: %w", key, err)
	}
	if cr.Namespace != expectedNamespace ||
		cr.Spec.SessionID != record.ID.String() ||
		cr.Spec.UserID != record.UserID.String() {
		return Target{}, ErrForbidden
	}
	if !phaseAllowsAttach(cr.Status.Phase) || cr.Status.PodName == "" {
		return Target{}, ErrNotRunning
	}
	expectedPodName := sessionidentity.PodName(record.ID.String())
	if cr.Status.PodName != expectedPodName {
		return Target{}, ErrForbidden
	}

	pod, err := k.core.CoreV1().Pods(expectedNamespace).Get(callCtx, cr.Status.PodName, metav1.GetOptions{})
	if err != nil {
		return Target{}, fmt.Errorf("get terminal Pod %s/%s: %w", expectedNamespace, cr.Status.PodName, err)
	}
	if pod.Namespace != expectedNamespace || pod.Name != expectedPodName ||
		pod.Labels["app.kubernetes.io/managed-by"] != "bosun" ||
		pod.Labels["bosun.io/user"] != record.UserID.String() ||
		pod.Labels["bosun.io/session"] != record.ID.String() ||
		!hasAgentContainer(pod) {
		return Target{}, ErrForbidden
	}
	return Target{
		SessionID: record.ID,
		UserID:    record.UserID,
		Namespace: expectedNamespace,
		CRName:    record.CRName,
		PodName:   expectedPodName,
	}, nil
}

func (k *KubernetesRuntime) Capture(ctx context.Context, target Target) ([]byte, error) {
	captureCtx, cancel := context.WithTimeout(ctx, captureCommandTimeout)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := k.exec(captureCtx, target, []string{
		"tmux", "capture-pane", "-p", "-e", "-S", "-", "-t", "bosun",
	}, nil, &stdout, &stderr, false, nil)
	if err != nil {
		return nil, fmt.Errorf("capture terminal pane: %w (%s)", err, stableExecError(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

func (k *KubernetesRuntime) Attach(
	ctx context.Context,
	target Target,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	sizes remotecommand.TerminalSizeQueue,
) error {
	if err := k.exec(ctx, target, []string{
		"tmux", "set-option", "-g", "terminal-overrides", "xterm*:smcup@:rmcup@",
		";", "attach-session", "-t", "bosun",
	}, stdin, stdout, stderr, true, sizes); err != nil {
		return fmt.Errorf("attach terminal session: %w", err)
	}
	return nil
}

func (k *KubernetesRuntime) UpdateActivity(
	ctx context.Context,
	target Target,
	at time.Time,
	minInterval time.Duration,
) error {
	callCtx, cancel := context.WithTimeout(ctx, kubernetesCallTimeout)
	defer cancel()
	key := types.NamespacedName{Namespace: target.Namespace, Name: target.CRName}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var cr bosunv1alpha1.AgentSession
		if err := k.crs.Get(callCtx, key, &cr); err != nil {
			return fmt.Errorf("get AgentSession before activity update: %w", err)
		}
		if cr.Spec.SessionID != target.SessionID.String() ||
			cr.Spec.UserID != target.UserID.String() ||
			!phaseAllowsAttach(cr.Status.Phase) {
			return ErrNotRunning
		}
		if previous, parseErr := time.Parse(time.RFC3339, cr.Annotations[sessionidentity.LastActiveAnnotation]); parseErr == nil &&
			at.Before(previous.Add(minInterval)) {
			return nil
		}
		if cr.Annotations == nil {
			cr.Annotations = make(map[string]string, 1)
		}
		cr.Annotations[sessionidentity.LastActiveAnnotation] = at.UTC().Format(time.RFC3339)
		if err := k.crs.Update(callCtx, &cr); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("update terminal activity annotation: %w", err)
	}
	return nil
}

func (k *KubernetesRuntime) exec(
	ctx context.Context,
	target Target,
	command []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	tty bool,
	sizes remotecommand.TerminalSizeQueue,
) error {
	request := k.core.CoreV1().RESTClient().Post().
		Namespace(target.Namespace).
		Resource("pods").
		Name(target.PodName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: agentContainerName,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil && !tty,
			TTY:       tty,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(k.config, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("create Kubernetes exec executor: %w", err)
	}
	streamStderr := stderr
	if tty {
		streamStderr = nil
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: stdin, Stdout: stdout, Stderr: streamStderr, Tty: tty, TerminalSizeQueue: sizes,
	})
}

func phaseAllowsAttach(phase bosunv1alpha1.AgentSessionPhase) bool {
	return phase == bosunv1alpha1.AgentSessionPhaseRunning ||
		phase == bosunv1alpha1.AgentSessionPhaseIdle
}

func hasAgentContainer(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == agentContainerName {
			return true
		}
	}
	return false
}

func stableExecError(stderr []byte) string {
	if len(stderr) == 0 {
		return "exec_failed"
	}
	return "remote_command_failed"
}
