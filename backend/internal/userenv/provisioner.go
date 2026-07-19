package userenv

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

// Provisioner 幂等创建用户环境 CR，并可列出已存在环境以支撑修复循环。
type Provisioner interface {
	// Ensure 幂等创建给定用户的 UserEnvironment CR；已存在时视为成功。
	Ensure(ctx context.Context, userID string) error
	// ExistingUserIDs 返回集群中已有 UserEnvironment CR 的用户 ID 集合。
	ExistingUserIDs(ctx context.Context) (map[string]struct{}, error)
}

// CRProvisioner 通过 controller-runtime typed client 创建 UserEnvironment CR（spec/02：禁止拼 kubectl）。
type CRProvisioner struct {
	client client.Client
}

// NewCRProvisioner 构造基于 k8s client 的 provisioner。
func NewCRProvisioner(c client.Client) *CRProvisioner {
	return &CRProvisioner{client: c}
}

// Ensure 创建 UserEnvironment CR；CR 与 label 只含 userID，不含邮箱（techspec §4.1）。
func (p *CRProvisioner) Ensure(ctx context.Context, userID string) error {
	ue := &bosunv1alpha1.UserEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: CRName(userID),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "bosun",
				"bosun.io/user":                userID,
			},
		},
		Spec: bosunv1alpha1.UserEnvironmentSpec{
			UserID:       userID,
			Namespace:    Namespace(userID),
			QuotaProfile: "mvp",
		},
	}
	if err := p.client.Create(ctx, ue); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create UserEnvironment %s: %w", ue.Name, err)
	}
	return nil
}

// Phase 返回用户环境 CR 的 status.phase；CR 尚未创建时返回 Pending（尚在初始化）。
func (p *CRProvisioner) Phase(ctx context.Context, userID string) (string, error) {
	var ue bosunv1alpha1.UserEnvironment
	if err := p.client.Get(ctx, types.NamespacedName{Name: CRName(userID)}, &ue); err != nil {
		if apierrors.IsNotFound(err) {
			return string(bosunv1alpha1.UserEnvironmentPhasePending), nil
		}
		return "", fmt.Errorf("get UserEnvironment %s: %w", CRName(userID), err)
	}
	if ue.Status.Phase == "" {
		return string(bosunv1alpha1.UserEnvironmentPhasePending), nil
	}
	return string(ue.Status.Phase), nil
}

// ExistingUserIDs 列出所有 UserEnvironment CR 并返回其 spec.userID 集合。
func (p *CRProvisioner) ExistingUserIDs(ctx context.Context) (map[string]struct{}, error) {
	var list bosunv1alpha1.UserEnvironmentList
	if err := p.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list UserEnvironments: %w", err)
	}
	ids := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		ids[list.Items[i].Spec.UserID] = struct{}{}
	}
	return ids, nil
}
