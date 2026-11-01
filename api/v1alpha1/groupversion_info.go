// Package v1alpha1 定义 topo-rdma的k8s api
// +kubebuilder:object:generate=true
// +groupName=topo.aiinfra.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion = schema.GroupVersion{
		Group:   "topo.aiinfra.dev",
		Version: "v1alpha1",
	}

	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	// 注册 CRD
	s.AddKnownTypes(GroupVersion,
		&NodeTopology{},
		&NodeTopologyList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
