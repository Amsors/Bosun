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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const agentContainerName = "agent"

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type QuiesceResult struct {
	CreatedAt        time.Time
	SizeBytes        int64
	SHA256           string
	AgentImageDigest string
}

type AgentQuiescer interface {
	Quiesce(context.Context, *corev1.Pod) (QuiesceResult, error)
}

type AgentWorkState string

const (
	AgentWorkStateUnknown          AgentWorkState = ""
	AgentWorkStateWorking          AgentWorkState = "working"
	AgentWorkStateAwaitingApproval AgentWorkState = "awaiting_approval"
	AgentWorkStateAwaitingChoice   AgentWorkState = "awaiting_choice"
	AgentWorkStateAwaitingInput    AgentWorkState = "awaiting_input"
	AgentWorkStateStopped          AgentWorkState = "stopped"
)

type AgentStateReader interface {
	ReadState(context.Context, *corev1.Pod) (AgentWorkState, error)
}

type KubernetesQuiescer struct {
	config *rest.Config
	core   kubernetes.Interface
}

func NewKubernetesQuiescer(config *rest.Config) (*KubernetesQuiescer, error) {
	if config == nil {
		return nil, fmt.Errorf("kubernetes REST config is required")
	}
	copy := rest.CopyConfig(config)
	coreClient, err := kubernetes.NewForConfig(copy)
	if err != nil {
		return nil, fmt.Errorf("create quiesce Kubernetes client: %w", err)
	}
	return &KubernetesQuiescer{config: copy, core: coreClient}, nil
}

func (q *KubernetesQuiescer) Quiesce(ctx context.Context, pod *corev1.Pod) (QuiesceResult, error) {
	if pod == nil || pod.Namespace == "" || pod.Name == "" {
		return QuiesceResult{}, fmt.Errorf("agent Pod identity is required")
	}
	digest := podAgentImageDigest(pod)
	request := q.core.CoreV1().RESTClient().Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: agentContainerName,
			Command:   []string{"/usr/local/bin/bosun-runtime-control", "quiesce", digest},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(q.config, "POST", request.URL())
	if err != nil {
		return QuiesceResult{}, fmt.Errorf("create agent quiesce executor: %w", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return QuiesceResult{}, fmt.Errorf("quiesce agent Pod: %w (%s)", err, stableQuiesceError(stderr.String()))
	}
	var response struct {
		CreatedAt        string `json:"createdAt"`
		SizeBytes        int64  `json:"sizeBytes"`
		SHA256           string `json:"sha256"`
		AgentImageDigest string `json:"agentImageDigest"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err != nil {
		return QuiesceResult{}, fmt.Errorf("parse agent quiesce response: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339, response.CreatedAt)
	if err != nil || response.SizeBytes < 0 || response.SizeBytes > 1024*1024 ||
		!sha256Pattern.MatchString(response.SHA256) || response.AgentImageDigest != digest {
		return QuiesceResult{}, fmt.Errorf("agent returned invalid recovery metadata")
	}
	return QuiesceResult{
		CreatedAt: createdAt.UTC(), SizeBytes: response.SizeBytes, SHA256: response.SHA256,
		AgentImageDigest: response.AgentImageDigest,
	}, nil
}

func (q *KubernetesQuiescer) ReadState(ctx context.Context, pod *corev1.Pod) (AgentWorkState, error) {
	if pod == nil || pod.Namespace == "" || pod.Name == "" {
		return AgentWorkStateUnknown, fmt.Errorf("agent Pod identity is required")
	}
	request := q.core.CoreV1().RESTClient().Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: agentContainerName,
			Command: []string{
				"/bin/bash", "-c",
				"cat /workspace/.bosun-state/agent-status 2>/dev/null || true",
			},
			Stdout: true,
			Stderr: true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(q.config, "POST", request.URL())
	if err != nil {
		return AgentWorkStateUnknown, fmt.Errorf("create agent state executor: %w", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return AgentWorkStateUnknown, fmt.Errorf("read agent state: %w (%s)", err, stableQuiesceError(stderr.String()))
	}
	state := AgentWorkState(strings.TrimSpace(stdout.String()))
	switch state {
	case AgentWorkStateUnknown, AgentWorkStateWorking, AgentWorkStateAwaitingApproval,
		AgentWorkStateAwaitingChoice, AgentWorkStateAwaitingInput, AgentWorkStateStopped:
		return state, nil
	default:
		return AgentWorkStateUnknown, fmt.Errorf("agent returned invalid work state")
	}
}

func podAgentImageDigest(pod *corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != agentContainerName {
			continue
		}
		imageID := strings.TrimPrefix(status.ImageID, "docker-pullable://")
		imageID = strings.TrimPrefix(imageID, "docker://")
		if at := strings.LastIndex(imageID, "@sha256:"); at >= 0 {
			return imageID[at+1:]
		}
		if strings.HasPrefix(imageID, "sha256:") {
			return imageID
		}
	}
	return ""
}

func stableQuiesceError(stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return "runtime_control_failed"
	}
	return "runtime_control_reported_error"
}
