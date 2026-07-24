// Package monitor 聚合 Kubernetes Pod、Node 与 metrics-server 的实时资源数据。
package monitor

import "time"

type Resources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
}

type ContainerSnapshot struct {
	Name     string     `json:"name"`
	Usage    *Resources `json:"usage"`
	Requests Resources  `json:"requests"`
	Limits   Resources  `json:"limits"`
}

type PodSnapshot struct {
	Namespace   string              `json:"namespace"`
	Name        string              `json:"name"`
	Phase       string              `json:"phase"`
	Resize      *PodResizeSnapshot  `json:"resize"`
	NodeName    string              `json:"nodeName"`
	Ready       bool                `json:"ready"`
	Restarts    int32               `json:"restarts"`
	CreatedAt   time.Time           `json:"createdAt"`
	Usage       *Resources          `json:"usage"`
	Requests    Resources           `json:"requests"`
	Limits      Resources           `json:"limits"`
	Containers  []ContainerSnapshot `json:"containers"`
	IsAgent     bool                `json:"isAgent"`
	SessionID   string              `json:"sessionID,omitempty"`
	SessionName string              `json:"sessionName,omitempty"`
	Username    string              `json:"username,omitempty"`
}

type PodResizeSnapshot struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type NodeSnapshot struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Roles       []string   `json:"roles"`
	Kubelet     string     `json:"kubeletVersion"`
	Usage       *Resources `json:"usage"`
	Capacity    Resources  `json:"capacity"`
	Allocatable Resources  `json:"allocatable"`
}

type SessionSnapshot struct {
	ObservedAt       time.Time   `json:"observedAt"`
	MetricsAvailable bool        `json:"metricsAvailable"`
	Pod              PodSnapshot `json:"pod"`
}

type ClusterSnapshot struct {
	ObservedAt           time.Time      `json:"observedAt"`
	PodMetricsAvailable  bool           `json:"podMetricsAvailable"`
	NodeMetricsAvailable bool           `json:"nodeMetricsAvailable"`
	Nodes                []NodeSnapshot `json:"nodes"`
	Pods                 []PodSnapshot  `json:"pods"`
}

type ResizeRequest struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
}

type AgentOwner struct {
	Username    string
	SessionName string
}
