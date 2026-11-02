package webhook

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	AnnotationRDMA             = "topo.aiinfra.dev/rdma"
	AnnotationPolicy           = "topo.aiinfra.dev/policy"
	AnnotationTargetContainers = "topo.aiinfra.dev/containers"
	AnnotationGIDIndex         = "topo.aiinfra.dev/gid-index"
	AnnotationSocketInterface  = "topo.aiinfra.dev/socket-interface"
	ResourceHCA                = corev1.ResourceName("rdma.aiinfra.dev/hca")

	environmentHCA = "NCCL_IB_HCA"
)

type PodMutator struct {
	Decoder  admission.Decoder
	Observer Observer
}

func (m *PodMutator) Handle(_ context.Context, request admission.Request) admission.Response {
	started := time.Now()
	result := resultError
	defer func() {
		// 结束时记录并更新metrics指标
		m.observer().ObserveRequest(result, time.Since(started))
	}()
	pod := &corev1.Pod{}
	// 解析准入请求的json为pod结构体
	if err := m.Decoder.Decode(request, pod); err != nil {
		return admission.Errored(400, err)
	}
	MutatePod(pod)
}

func MutatePod(pod *corev1.Pod) (bool, error) {
	if pod == nil {
		return false, fmt.Errorf("pod should not be empty")
	}
	candidate := pod.DeepCopy()
	changed, err := mutatePod(candidate)
	if err != nil || !changed {
		return changed, err
	}
	*pod = *candidate
	return true, nil
}

func (m *PodMutator) observer() Observer {
	if m.Observer == nil {
		return noopObserver{}
	}
	return m.Observer
}

func mutatePod(pod *corev1.Pod) (bool, error) {
	if pod.Annotations[AnnotationRDMA] != "enabled" {
		return false, nil
	}
	policy := pod.Annotations[AnnotationPolicy]
	if policy != "" && policy != "same-numa" {
		return false, fmt.Errorf("topology policy is not supported")
	}
	targets, err := targetContainers(pod)
	if err != nil {
		return false, err
	}
	desiredEnv, err := desiredEnvironment(pod.Annotations)
	if err != nil {
		return false, err
	}
	for _, index := range targets {
		// 禁止用户提前给pod注入NCCL_IB_HCA 应该由RDMA Device Plugin注入
		if err := rejectReservedEnvironment(&pod.Spec.Containers[index], environmentHCA); err != nil {
			return false, err
		}
	}
	if policy == "same-numa" {
		if err := validateSameNUMAWorkload(pod, targets); err != nil {
			return false, err
		}
	}
}

// targetContainers 获取目标容器 (返回在目标容器在.Spec.Containers[]中的索引)
// 目标容器指需要注入RDMA资源和NCCL配置的容器
func targetContainers(pod *corev1.Pod) ([]int, error) {
	// 先读取annotation中是否显式指定了目标容器 例如 "trainer,worker"
	if configured := strings.TrimSpace(pod.Annotations[AnnotationTargetContainers]); configured != "" {
		// 记录pod实际容器->索引的映射
		indexByName := make(map[string]int, len(pod.Spec.Containers))
		for index := range pod.Spec.Containers {
			indexByName[pod.Spec.Containers[index].Name] = index
		}
		var result []int
		// 用于去重
		seen := make(map[int]struct{})
		for _, name := range strings.Split(configured, ",") {
			name = strings.TrimSpace(name)
			index, exists := indexByName[name]
			if !exists {
				return nil, fmt.Errorf("target container %q does not exist", name)
			}
			// 之前没记录过 则添加到结果
			if _, duplicate := seen[index]; !duplicate {
				result = append(result, index)
				seen[index] = struct{}{}
			}
		}
		// 若显式指定了 则不触发自动寻找GPU容器
		return result, nil
	}
	// 若annotation中没有显式指定目标容器 则尝试自动寻找申请了GPU的容器
	var gpuContainers []int
	for index, container := range pod.Spec.Containers {
		for resourceName, quantity := range container.Resources.Limits {
			// 遍历申请的不同资源 找资源名以"/gpu"结尾且数量大于0的 例如"nvidia.com/gpu: 1"
			if strings.HasSuffix(string(resourceName), "/gpu") && quantity.Sign() > 0 {
				gpuContainers = append(gpuContainers, index)
				break
			}
		}
	}
	if len(gpuContainers) > 0 {
		return gpuContainers, nil
	}
	if len(pod.Spec.Containers) == 1 {
		return []int{0}, nil
	}
	return nil, fmt.Errorf("multi-container pod does not specify %s, and target containers cannot be determined from GPU limits", AnnotationTargetContainers)
}

// desiredEnvironment 根据pod的annotations计算要为目标容器设置的NCCL环境变量
func desiredEnvironment(annotations map[string]string) (map[string]string, error) {
	// 默认允许NCCL使用IB/RDMA路径 (不等于一定会使用)
	result := map[string]string{"NCCL_IB_DISABLE": "0"}
	// 读取annotation中指定的gid-index
	if gid := strings.TrimSpace(annotations[AnnotationGIDIndex]); gid != "" {
		value, err := strconv.Atoi(gid)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("%s must be a non-negative integer", AnnotationGIDIndex)
		}
		result["NCCL_IB_GID_INDEX"] = gid
	}
	// 读取annotation中指定的网络接口名
	socketInterface := strings.TrimSpace(annotations[AnnotationSocketInterface])
	if socketInterface != "" {
		result["NCCL_SOCKET_IFNAME"] = socketInterface
	}
	return result, nil
}

func rejectReservedEnvironment(container *corev1.Container, name string) error {
	for _, variable := range container.Env {
		if variable.Name == name {
			return fmt.Errorf("container %s must not set %s, which is managed by the Device Plugin", container.Name, name)
		}
	}
	return nil
}

func validateSameNUMAWorkload(pod *corev1.Pod, targets []int) error {
	if pod.Spec.Resources != nil {
		return fmt.Errorf("same-numa does not currently support Pod-Level resources; " +
			"please use Container_Level CPU and Memory requests and limits")
	}
	// 进行 Guaranteed Qos 校验
	// 校验initContainers
	for index := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[index]
		if err := validateGuaranteedContainer(container, "initContainer"); err != nil {
			return err
		}
	}
	// 校验Containers
	for index := range pod.Spec.Containers {
		container := &pod.Spec.Containers[index]
		if err := validateGuaranteedContainer(container, "Container"); err != nil {
			return err
		}
	}
	// 校验目标容器的CPU核心数必须为正整数
	for _, index := range targets {
		container := &pod.Spec.Containers[index]
		cpu := container.Resources.Requests[corev1.ResourceCPU]
		value, exact := cpu.AsInt64()
		if !exact || value <= 0 {
			return fmt.Errorf("target container %s cpu request must be non-negative integer, for CPU Manager topology allocation")
		}
	}
	return nil
}

// validateGuaranteedContainer 对容器进行 Guaranteed Qos 校验
func validateGuaranteedContainer(container *corev1.Container, kind string) error {
	for _, resourceName := range []corev1.ResourceName{
		corev1.ResourceCPU,
		corev1.ResourceMemory,
	} {
		request, hasRequest := container.Resources.Requests[resourceName]
		limit, hasLimit := container.Resources.Limits[resourceName]
		// Guaranteed Qos 要求CPU和Memory的 request==limit 且 request,limit > 0
		// request,limit > 0
		if !hasRequest || request.Sign() <= 0 || !hasLimit || limit.Sign() <= 0 {
			return fmt.Errorf(
				"%s %s must set non-negative %s request and limit, to ensure pod Guaranteed Qos",
				kind,
				container.Name,
				resourceName,
			)
		}
		// request == limit
		if request.Cmp(limit) != 0 {
			return fmt.Errorf(
				"%s %s must set %s request == limit, to ensure pod Guaranteed Qos",
				kind,
				container.Name,
				resourceName,
			)
		}
	}
	return nil
}
