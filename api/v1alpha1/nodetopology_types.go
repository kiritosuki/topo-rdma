package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MinDiscoveryInterval = 5 * time.Second
	MaxDiscoveryInterval = 10 * time.Minute
)

type NodeTopologySpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeName 不可变"
	// NodeName 不可变
	NodeName string `json:"nodeName"`
	// +kubebuilder:default="30s"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('5s') && duration(self) <= duration('10m')",message="discoveryInterval 必须位于 5s 到 10m 之间"
	// DiscoveryInterval 必须位于 5s 到 10m 之间
	DiscoveryInterval metav1.Duration `json:"discoveryInterval,omitempty"`
}

type PCIDevice struct {
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	PCIAddress string `json:"pciAddress"`
	VendorID   string `json:"vendorID,omitempty"`
}

type RDMADevice struct {
	// +kubebuilder:validation:MinLength=1
	Name       string `json:"name"`
	PCIAddress string `json:"pciAddress,omitempty"`
	// +listType=set
	NetDevices []string `json:"netDevices,omitempty"`
}

type NUMANode struct {
	// +kubebuilder:validation:Minimum=0
	ID int32 `json:"id"`
	// +kubebuilder:validation:MinLength=1
	CPUs string `json:"cpus"`
	// +listType=map
	// +listMapKey=id
	GPUs []PCIDevice `json:"gpus,omitempty"`
	// +listType=map
	// +listMapKey=name
	RDMADevices []RDMADevice `json:"rdmaDevices,omitempty"`
}

type NodeTopologyStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	LastDiscoveryTime  *metav1.Time `json:"lastDiscoveryTime,omitempty"`
	// +listType=map
	// +listMapKey=id
	NUMANodes []NUMANode `json:"numaNodes,omitempty"`
	// +listType=map
	// +listMapKey=id
	UnassignedGPUs []PCIDevice `json:"unassignedGPUs,omitempty"`
	// +listType=map
	// +listMapKey=name
	UnassignedRDMA []RDMADevice `json:"unassignedRDMA,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ntopo
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath='.spec.nodeName'
// +kubebuilder:printcolumn:name="Discovered",type=string,JSONPath=`.status.conditions[?(@.type=="Discovered")].status`
// +kubebuilder:printcolumn:name="Fresh",type=string,JSONPath=`.status.conditions[?(@.type=="Fresh")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NodeTopology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeTopologySpec   `json:"spec,omitempty"`
	Status            NodeTopologyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeTopologyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeTopology `json:"items"`
}
