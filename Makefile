CONTROLLER_GEN_VERSION ?= v0.22.0
CONTROLLER_GEN := $(CURDIR)/bin/controller-tools/$(CONTROLLER_GEN_VERSION)/controller-gen

.PHONY: generate crd fmt

$(CONTROLLER_GEN):
	mkdir -p "$(dir $(CONTROLLER_GEN))"
	GOBIN="$(dir $(CONTROLLER_GEN))" go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

generate: $(CONTROLLER_GEN)
	"$(CONTROLLER_GEN)" object paths="./api/..."

crd: generate
	"$(CONTROLLER_GEN)" crd paths="./api/..." output:crd:artifacts:config=config/crd

clean:
	$(RM) api/v1alpha1/zz_generated.deepcopy.go config/crd/topo.aiinfra.dev_nodetopologies.yaml
	$(RM) topo-rdma-preflight topology-agent controller-manager rdma-device-plugin webhook preflight ./*.test ./*.out ./*.coverprofile ./*.prof ./*.pprof
	rm -rf bin dist coverage

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')