package controllers

import (
	"context"
	"fmt"
	"time"

	topov1alpha1 "github.com/kiritosuki/topo-rdma/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ConditionFresh     = "Fresh"
	MaxStaleMultiplier = 100

	defaultAgentInterval   = 30 * time.Second
	defaultStaleMultiplier = 3
)

type NodeTopologyReconciler struct {
	client.Client
	Now             func() time.Time
	StaleMultiplier int
	Observer        Observer
}

func (r *NodeTopologyReconciler) Reconcile(ctx context.Context, request ctrl.Request) (result ctrl.Result, err error) {
	started := time.Now()
	var observedStatus *metav1.ConditionStatus
	defer func() {
		r.observer().ObserveReconcile(request.Name, time.Since(started), observedStatus, err)
	}()
	resource := &topov1alpha1.NodeTopology{}
	// 从k8s获取资源对象
	if err := r.Get(ctx, request.NamespacedName, resource); err != nil {
		// 如果找不到 就清理prometheus资源 不返回错误
		if apierrors.IsNotFound(err) {
			r.observer().ForgetNode(request.Name)
			return ctrl.Result{}, nil
		}
		// 获取出错
		return ctrl.Result{}, err
	}
	// 获取资源对象成功
	now := time.Now()
	// 这个now只是方便测试时指定时间
	if r.Now != nil {
		now = r.Now()
	}
	// 计算期望状态
	condition, requeueAfter := freshnessCondition(resource, now, r.staleMultiplier())
	// 找出Fresh类型的Condition (Condition的Type是唯一的)
	// 获取现有状态
	existing := meta.FindStatusCondition(resource.Status.Conditions, ConditionFresh)
	if sameCondition(existing, condition) {
		// 更新observedStatus 便于prometheus更新metrics
		observedStatus = &condition.Status
		// 状态一致 无需更新
		return ctrl.Result{
			RequeueAfter: requeueAfter,
		}, nil
	}
	// 状态不一致 更新状态
	meta.SetStatusCondition(&resource.Status.Conditions, condition)
	if err := r.Status().Update(ctx, resource); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{
				Requeue: true,
			}, nil
		}
		return ctrl.Result{}, fmt.Errorf("update NodeTopology freshness: %w", err)
	}
	// 更新observedStatus 便于prometheus更新metrics
	observedStatus = &condition.Status
	return ctrl.Result{
		RequeueAfter: requeueAfter,
	}, nil
}

// SetupWithManager 注册conciler
func (r *NodeTopologyReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&topov1alpha1.NodeTopology{}).
		Complete(r)
}

func (r *NodeTopologyReconciler) observer() Observer {
	if r.Observer == nil {
		return noopObserver{}
	}
	return r.Observer
}

func (r *NodeTopologyReconciler) staleMultiplier() int {
	if r.StaleMultiplier > 0 {
		return r.StaleMultiplier
	}
	return defaultStaleMultiplier
}

// freshnessCondition 获取freshnessCondition 返回下次controller协调的时间duration
func freshnessCondition(resource *topov1alpha1.NodeTopology, now time.Time, multiplier int) (metav1.Condition, time.Duration) {
	condition := metav1.Condition{
		Type:               ConditionFresh,
		ObservedGeneration: resource.Generation,
	}
	interval := resource.Spec.DiscoveryInterval.Duration
	if interval == 0 {
		interval = defaultAgentInterval
	}
	if interval < topov1alpha1.MinDiscoveryInterval || interval > topov1alpha1.MaxDiscoveryInterval {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = "InvalidDiscoveryInterval"
		condition.Message = "discovery interval is outside the allowed range"
		return condition, defaultAgentInterval
	}
	if multiplier < 1 || multiplier > MaxStaleMultiplier {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = "InvalidDiscoveryMultiplier"
		condition.Message = "stale multiplier is outside the allowed range"
		return condition, interval
	}
	threshold := interval * time.Duration(multiplier)
	if resource.Status.LastDiscoveryTime == nil {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = "DiscoveryPending"
		condition.Message = "node topology has not been discovered yet"
		return condition, interval
	}
	age := now.Sub(resource.Status.LastDiscoveryTime.Time)
	if age < 0 {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = "ClockSkewDetected"
		condition.Message = "last discovery time is in the future, please check node clock synchronization"
		return condition, interval
	}
	if age >= threshold {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "DiscoveryStale"
		condition.Message = "node topology has not been updated within the expected time"
		return condition, interval
	}
	condition.Status = metav1.ConditionTrue
	condition.Reason = "DiscoveryFresh"
	condition.Message = "node topology is up to date"
	return condition, threshold - age
}

func sameCondition(current *metav1.Condition, desired metav1.Condition) bool {
	return current != nil &&
		current.Status == desired.Status &&
		current.Reason == desired.Reason &&
		current.Message == desired.Message &&
		current.ObservedGeneration == desired.ObservedGeneration
}
