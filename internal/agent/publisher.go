package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	topov1alpha1 "github.com/kiritosuki/topo-rdma/api/v1alpha1"
	"github.com/kiritosuki/topo-rdma/internal/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const conditionDiscovered = "Discovered"

type Discoverer interface {
	Discover() (topology.Snapshot, error)
}

type Publisher struct {
	Client     client.Client
	Discoverer Discoverer
	NodeName   string
	Interval   time.Duration
	Logger     *slog.Logger
	Observer   Observer
}

func (p *Publisher) Run(ctx context.Context) error {
	if err := p.validate(); err != nil {
		return err
	}
	if err := p.PublishOnce(ctx); err != nil {
		p.Logger.Error("initial topology publication failed", "error", err)
	}
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.PublishOnce(ctx); err != nil {
				p.Logger.Error("topology publication failed", "error", err)
			}
		}
	}
}

func (p *Publisher) PublishOnce(ctx context.Context) (err error) {
	if err = p.validate(); err != nil {
		return err
	}
	started := time.Now()
	var snapshot topology.Snapshot
	defer func() {
		// 结束时记录并更新metrics指标
		p.Observer.ObservePublication(time.Since(started), snapshot, err)
	}()
	// 发现硬件设备
	snapshot, err = p.Discoverer.Discover()
	if err != nil {
		// 发现硬件设备失败
		discoveryErr := fmt.Errorf("discover node topology: %w", err)
		// 更新k8s资源中NodeTopology的状态
		return errors.Join(discoveryErr, p.publishDiscoveryFailure(ctx))
	}
	// 发现硬件设备成功
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		topologyResource, err := p.getOrCreate(ctx)
		if err != nil {
			return err
		}
		// 更新k8s资源对象
		topologyResource.Status.ObservedGeneration = topologyResource.Generation
		now := metav1.Now()
		topologyResource.Status.LastDiscoveryTime = &now
		topologyResource.Status.NUMANodes = convertNUMANodes(snapshot.NUMANodes)
		topologyResource.Status.UnassignedGPUs = convertGPUs(snapshot.UnassignedGPUs)
		topologyResource.Status.UnassignedRDMA = convertRDMA(snapshot.UnassignedRDMA)
		meta.SetStatusCondition(&topologyResource.Status.Conditions, metav1.Condition{
			Type:               conditionDiscovered,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: topologyResource.Generation,
			Reason:             "DiscoverySucceeded",
			Message:            "host topology was read from Linux sysfs",
		})
		// 每次成功publish都会更新LastDiscoveryTime 用于区分"硬件设备没有变化"和"agent停止工作"
		if err := p.Client.Status().Update(ctx, topologyResource); err != nil {
			return fmt.Errorf("update NodeTopology status: %w", err)
		}
		return nil
	})
}

// validate 对Publisher进行初始化校验
func (p *Publisher) validate() error {
	if p.Client == nil {
		return fmt.Errorf("client is required")
	}
	if p.Discoverer == nil {
		return fmt.Errorf("discoverer is required")
	}
	if p.NodeName == "" {
		return fmt.Errorf("node name is required")
	}
	if p.Interval < topov1alpha1.MinDiscoveryInterval || p.Interval > topov1alpha1.MaxDiscoveryInterval {
		return fmt.Errorf(
			"discovery interval %s is outside %s ~ %s",
			p.Interval,
			topov1alpha1.MinDiscoveryInterval,
			topov1alpha1.MaxDiscoveryInterval,
		)
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	if p.Observer == nil {
		p.Observer = noopObserver{}
	}
	return nil
}

// publishDiscoveryFailure 在发现硬件设备失败时调用 用于更新NodeTopology资源对象的状态
// 仅修改.Status.Conditions 因此会保留最近一次成功发现的硬件快照和发现时间
func (p *Publisher) publishDiscoveryFailure(ctx context.Context) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		topologyResource, err := p.getOrCreate(ctx)
		if err != nil {
			return err
		}
		meta.SetStatusCondition(&topologyResource.Status.Conditions, metav1.Condition{
			Type:               conditionDiscovered,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: topologyResource.Generation,
			Reason:             "DiscoveryFailed",
			Message:            "failed to get linux node topology, please check agent log",
		})
		if err := p.Client.Status().Update(ctx, topologyResource); err != nil {
			return fmt.Errorf("update NodeTopology discovery failure: %w", err)
		}
		return nil
	})
}

// getOrCreate 从k8s获取NodeTopology资源对象 若找不到就创建
func (p *Publisher) getOrCreate(ctx context.Context) (*topov1alpha1.NodeTopology, error) {
	key := types.NamespacedName{
		Name: p.NodeName,
	}
	resource := &topov1alpha1.NodeTopology{}
	if err := p.Client.Get(ctx, key, resource); err != nil {
		// 没查到NodeTopology资源对象
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get NodeTopology %s: %w", p.NodeName, err)
		}
		// 查NodeTopology资源对象出了其他错误 则新建资源对象
		resource = &topov1alpha1.NodeTopology{
			ObjectMeta: metav1.ObjectMeta{
				Name: p.NodeName,
			},
			Spec: topov1alpha1.NodeTopologySpec{
				NodeName: p.NodeName,
				DiscoveryInterval: metav1.Duration{
					Duration: p.Interval,
				},
			},
		}
		if err := p.Client.Create(ctx, resource); err != nil {
			// 如果新建资源时已经存在 则递归调用 下次Get即可拿到
			if apierrors.IsAlreadyExists(err) {
				return p.getOrCreate(ctx)
			}
			// 新建资源失败
			return nil, fmt.Errorf("create NodeTopology %s: %w", p.NodeName, err)
		}
		// 新建资源成功 返回
		return resource, nil
	}
	// 已有资源对象
	if resource.Spec.NodeName != p.NodeName {
		return nil, fmt.Errorf("NodeTopology %s targets node %q", resource.Name, resource.Spec.NodeName)
	}
	if resource.Spec.DiscoveryInterval.Duration != p.Interval {
		resource.Spec.DiscoveryInterval = metav1.Duration{
			Duration: p.Interval,
		}
		if err := p.Client.Update(ctx, resource); err != nil {
			return nil, fmt.Errorf("update NodeTopology %s discovery interval: %w", p.NodeName, err)
		}
	}
	return resource, nil
}

func convertNUMANodes(nodes []topology.NUMANode) []topov1alpha1.NUMANode {
	result := make([]topov1alpha1.NUMANode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, topov1alpha1.NUMANode{
			ID:          int32(node.ID),
			CPUs:        node.CPUs,
			GPUs:        convertGPUs(node.GPUs),
			RDMADevices: convertRDMA(node.RDMADevices),
		})
	}
	return result
}

func convertGPUs(devices []topology.PCIDevice) []topov1alpha1.PCIDevice {
	result := make([]topov1alpha1.PCIDevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, topov1alpha1.PCIDevice{
			ID:         device.ID,
			PCIAddress: device.PCIAddress,
			VendorID:   device.VendorID,
		})
	}
	return result
}

func convertRDMA(devices []topology.RDMADevice) []topov1alpha1.RDMADevice {
	result := make([]topov1alpha1.RDMADevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, topov1alpha1.RDMADevice{
			Name:       device.Name,
			PCIAddress: device.PCIAddress,
			NetDevices: append([]string{}, device.NetDevices...),
		})
	}
	return result
}
