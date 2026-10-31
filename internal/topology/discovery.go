package topology

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"k8s.io/utils/cpuset"
)

const nvidiaVendorID = "0x10de"

var pciAddressPattern = regexp.MustCompile(`^[[:xdigit:]]{4}:[[:xdigit:]]{2}:[[:xdigit:]]{2}\.[[:xdigit:]]$`)

type PCIDevice struct {
	ID         string `json:"id"`
	PCIAddress string `json:"PCIAddress"`
	VendorID   string `json:"venderID,omitempty"`
}

type RDMADevice struct {
	Name       string   `json:"name"`
	PCIAddress string   `json:"pciAddress,omitempty"`
	NetDevices []string `json:"netDevices,omitempty"`
}

type NUMANode struct {
	ID          int          `json:"id"`
	CPUs        string       `json:"cpus"`
	GPUs        []PCIDevice  `json:"gpus"`
	RDMADevices []RDMADevice `json:"rdmaDevices,omitempty"`
}

type Snapshot struct {
	NUMANodes      []NUMANode   `json:"NUMANodes"`
	UnassignedGPUs []PCIDevice  `json:"unassignedGPUs,omitempty"`
	UnassignedRDMA []RDMADevice `json:"unassignedRDMA,omitempty"`
}

type Discoverer struct {
	Root string
}

func NewDiscoverer() *Discoverer {
	return &Discoverer{Root: "/"}
}

type locatedGPU struct {
	numa   int
	device PCIDevice
}

type locatedRDMA struct {
	numa   int
	device RDMADevice
}

func (d *Discoverer) Discover() (Snapshot, error) {
	// 发现numa节点
	nodes, err := d.discoverNumaNodes()
	if err != nil {
		return Snapshot{}, err
	}
	index := make(map[int]int, len(nodes))
	for i := range nodes {
		index[nodes[i].ID] = i
	}
	// 发现GPU
	gpus, err := d.discoverGPUs()
	if err != nil {
		return Snapshot{}, err
	}
	// 发现RDMA
	rdmaDevices, err := d.discoverRDMA()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		NUMANodes: nodes,
	}
	// 添加发现的GPU
	for _, device := range gpus {
		if nodeIndex, ok := index[device.numa]; ok {
			snapshot.NUMANodes[nodeIndex].GPUs = append(snapshot.NUMANodes[nodeIndex].GPUs, device.device)
		} else {
			snapshot.UnassignedGPUs = append(snapshot.UnassignedGPUs, device.device)
		}
	}
	// 添加发现的RDMA
	for _, device := range rdmaDevices {
		if nodeIndex, ok := index[device.numa]; ok {
			snapshot.NUMANodes[nodeIndex].RDMADevices = append(snapshot.NUMANodes[nodeIndex].RDMADevices, device.device)
		} else {
			snapshot.UnassignedRDMA = append(snapshot.UnassignedRDMA, device.device)
		}
	}
	sortSnapshot(&snapshot)
	return snapshot, nil
}

func (d *Discoverer) discoverNumaNodes() ([]NUMANode, error) {
	// 列出所有numa node
	paths, err := filepath.Glob(d.path("/sys/devices/system/node/node[0-9]*"))
	if err != nil {
		return nil, fmt.Errorf("glob NUMA nodes: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no NUMA nodes found under %s", d.path("/sys/devices/system/node"))
	}
	nodes := make([]NUMANode, 0, len(paths))
	// path为每个numa node的路径
	for _, path := range paths {
		// 提取node编号 例如 node1 -> 1
		idText := strings.TrimPrefix(filepath.Base(path), "node")
		id, err := strconv.Atoi(idText)
		if err != nil {
			return nil, fmt.Errorf("parse NUMA node %q: %w", filepath.Base(path), err)
		}
		cpus, err := readTrimmed(filepath.Join(path, "cpulist"))
		if err != nil {
			return nil, fmt.Errorf("parse NUMA node %q: %w", filepath.Base(path), err)
		}
		if err := validateCPUList(cpus); err != nil {
			return nil, fmt.Errorf("invalid CPU list for NUMA node %d: %w", id, err)
		}
		nodes = append(nodes, NUMANode{
			ID:   id,
			CPUs: cpus,
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nil
}

func (d *Discoverer) discoverGPUs() ([]locatedGPU, error) {
	// 列出所有PCI设备 目录一般为PCI BDF地址
	paths, err := filepath.Glob(d.path("/sys/bus/pci/devices/*"))
	if err != nil {
		return nil, fmt.Errorf("glob PCI devices: %w", err)
	}
	var result []locatedGPU
	for _, path := range paths {
		// 读取PCI设备目录下的vendor
		vendor, err := readTrimmed(filepath.Join(path, "vendor"))
		if err != nil {
			// 没有vendor信息 可能是非PCI设备目录 跳过
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read PCI vendor for %s: %w", filepath.Base(path), err)
		}
		// 读取PCI设备目录下的class
		class, err := readTrimmed(filepath.Join(path, "class"))
		if err != nil {
			return nil, fmt.Errorf("read GPU NUMA node for %s: %w", filepath.Base(path), err)
		}
		if !isGPU(vendor, class) {
			continue
		}
		// 读取PCI设备目录下的numa_node
		numa, err := readNUMANode(filepath.Join(path, "numa_node"))
		if err != nil {
			return nil, fmt.Errorf("read GPU NUMA node for %s: %w", filepath.Base(path), err)
		}
		address := filepath.Base(path)
		result = append(result, locatedGPU{
			// 如果numa_node文件不存在 这里numa=-1
			numa: numa,
			device: PCIDevice{
				ID:         address,
				PCIAddress: address,
				VendorID:   strings.ToLower(vendor),
			},
		})
	}
	return result, nil
}

func (d *Discoverer) discoverRDMA() ([]locatedRDMA, error) {
	// 列出所有RDMA网卡目录
	paths, err := filepath.Glob(d.path("/sys/class/infiniband/*"))
	if err != nil {
		return nil, fmt.Errorf("glob RDMA devices: %w", err)
	}
	var result []locatedRDMA
	for _, path := range paths {
		name := filepath.Base(path)
		// 读取RDMA网卡目录下的numa_node
		numa, err := readNUMANode(filepath.Join(path, "device", "numa_node"))
		if err != nil {
			return nil, fmt.Errorf("read RDMA NUMA node for %s: %w", name, err)
		}
		// 获取PCI设备的BDF地址
		pciAddress, err := pciAddressForDevice(filepath.Join(path, "device"))
		if err != nil {
			return nil, fmt.Errorf("resolve RDMA PCI device for %s: %w", name, err)
		}
		// 获取网卡设备名集合
		netDevices, err := directoryNames(filepath.Join(path, "device", "net"))
		result = append(result, locatedRDMA{
			numa: numa,
			device: RDMADevice{
				Name:       name,
				PCIAddress: pciAddress,
				NetDevices: netDevices,
			},
		})
	}
	return result, nil
}

func (d *Discoverer) path(absolute string) string {
	if d.Root == "" || d.Root == "/" {
		return absolute
	}
	return filepath.Join(d.Root, strings.TrimPrefix(absolute, "/"))
}

func readTrimmed(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func validateCPUList(value string) error {
	set, err := cpuset.Parse(value)
	if err != nil {
		return fmt.Errorf("parse cpu list %q: %w", value, err)
	}
	if set.Size() == 0 {
		return fmt.Errorf("cpu list should be not empty")
	}
	return nil
}

// isGPU 目前逻辑只能识别Nvidia厂商的GPU设备
func isGPU(vendor string, class string) bool {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	class = strings.ToLower(strings.TrimSpace(class))
	// 0x03是显示控制器 0x12是处理加速器 GPU通常包含在这两类中
	return vendor == nvidiaVendorID && (strings.HasPrefix(class, "0x03") || strings.HasPrefix(class, "0x12"))
}

func readNUMANode(path string) (int, error) {
	value, err := readTrimmed(path)
	if err != nil {
		if os.IsNotExist(err) {
			return -1, nil
		}
		return 0, err
	}
	numa, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", value, err)
	}
	return numa, nil
}

func pciAddressForDevice(path string) (string, error) {
	// 软连接 -> 返回PCI设备目录的真实路径
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
	}
	// PCI BDF地址
	address := filepath.Base(resolved)
	if !pciAddressPattern.MatchString(address) {
		return "", nil
	}
	return address, nil
}

func directoryNames(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func sortSnapshot(snapshot *Snapshot) {
	for i := range snapshot.NUMANodes {
		// 对每个NUMA节点的GPUs排序
		sort.Slice(snapshot.NUMANodes[i].GPUs, func(a, b int) bool {
			return snapshot.NUMANodes[i].GPUs[a].PCIAddress < snapshot.NUMANodes[i].GPUs[b].PCIAddress
		})
		// 对每个NUMA节点的RDMA设备排序
		sort.Slice(snapshot.NUMANodes[i].RDMADevices, func(a, b int) bool {
			return snapshot.NUMANodes[i].RDMADevices[a].Name < snapshot.NUMANodes[i].RDMADevices[b].Name
		})
	}
}
