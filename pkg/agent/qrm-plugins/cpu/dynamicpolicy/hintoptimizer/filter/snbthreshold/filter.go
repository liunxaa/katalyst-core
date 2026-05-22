/*
Copyright 2022 The Katalyst Authors.

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

package snbthreshold

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// HintFilterNameSNBCPUTotalRequestThreshold is the registry name for the SNB
// CPU total request threshold filter.
const HintFilterNameSNBCPUTotalRequestThreshold = "snb_cpu_total_request_threshold"

// GetContainerRequestedCoresFunc returns the requested cpu cores for a single
// allocation. It is injected by the caller to keep this filter decoupled from
// DynamicPolicy.
type GetContainerRequestedCoresFunc = state.GetContainerRequestedCoresFunc

// Options contains the dependencies required by the SNB threshold filter.
type Options struct {
	State                            state.State
	ReservedCPUs                     machine.CPUSet
	MachineInfo                      *machine.KatalystMachineInfo
	GetContainerRequestedCores       GetContainerRequestedCoresFunc
	SNBCPUTotalRequestThresholdRatio float64
}

// SNBCPUTotalRequestThresholdFilter rejects topology hints whose target NUMA
// already accumulates SNB+DNB cpu request beyond the configured ratio of the
// allocatable cpu quantity (excluding reserved cpus). The filter is a no-op
// when the ratio is zero or non-positive.
type SNBCPUTotalRequestThresholdFilter struct {
	state                      state.State
	reservedCPUs               machine.CPUSet
	machineInfo                *machine.KatalystMachineInfo
	getContainerRequestedCores GetContainerRequestedCoresFunc
	cpuTotalRequestThreshold   float64
}

var _ hintoptimizer.HintFilter = &SNBCPUTotalRequestThresholdFilter{}

// New returns a new SNBCPUTotalRequestThresholdFilter.
func New(opts Options) *SNBCPUTotalRequestThresholdFilter {
	return &SNBCPUTotalRequestThresholdFilter{
		state:                      opts.State,
		reservedCPUs:               opts.ReservedCPUs,
		machineInfo:                opts.MachineInfo,
		getContainerRequestedCores: opts.GetContainerRequestedCores,
		cpuTotalRequestThreshold:   opts.SNBCPUTotalRequestThresholdRatio,
	}
}

// NewFromFactoryOptions adapts policy.HintOptimizerFactoryOptions to a
// HintFilter, so that registry.SharedCoresHintFilterRegistry can construct
// this filter via the shared factory plumbing.
func NewFromFactoryOptions(opts policy.HintOptimizerFactoryOptions) (hintoptimizer.HintFilter, error) {
	return New(Options{
		State:                            opts.State,
		ReservedCPUs:                     opts.ReservedCPUs,
		MachineInfo:                      opts.MachineInfo,
		GetContainerRequestedCores:       opts.GetContainerRequestedCores,
		SNBCPUTotalRequestThresholdRatio: opts.SNBCPUTotalRequestThresholdRatio,
	}), nil
}

// Run is a no-op for this stateless filter.
func (f *SNBCPUTotalRequestThresholdFilter) Run(_ <-chan struct{}) error { return nil }

// Filter prunes topology hints that would push SNB+DNB cpu total request
// beyond the configured threshold. It mutates `hints` in place.
func (f *SNBCPUTotalRequestThresholdFilter) Filter(req hintoptimizer.Request, hints *pluginapi.ListOfTopologyHints) error {
	if req.ResourceRequest == nil {
		return fmt.Errorf("got nil request")
	}
	if f.cpuTotalRequestThreshold <= 0 {
		return nil
	}
	if f.cpuTotalRequestThreshold > 1 {
		return fmt.Errorf("invalid shared_cores numa_binding cpu total request threshold ratio: %.3f", f.cpuTotalRequestThreshold)
	}
	if hints == nil {
		return nil
	}
	if len(hints.Hints) == 0 {
		return cpuutil.ErrNoAvailableCPUHints
	}

	machineState := f.state.GetMachineState()
	filteredTopologyHints := make([]*pluginapi.TopologyHint, 0, len(hints.Hints))
	scope := ""
	for _, hint := range hints.Hints {
		if hint == nil {
			continue
		}

		hintNUMASet, err := machine.NewCPUSetUint64(hint.Nodes...)
		if err != nil {
			return err
		}

		totalAllocatable := float64(f.machineInfo.CPUDetails.CPUsInNUMANodes(hintNUMASet.ToSliceInt()...).Difference(f.reservedCPUs).Size())
		totalRequestedQuantity := f.getSNBCPUTotalRequest(req.ResourceRequest, req.CPURequest, machineState, hintNUMASet)
		existingRequestedQuantity := totalRequestedQuantity - req.CPURequest

		if checkErr := f.checkThreshold(req.ResourceRequest, totalRequestedQuantity, totalAllocatable, fmt.Sprintf("numa:%s", hintNUMASet.String())); checkErr != nil {
			general.Warningf("filter out topology hint %v for pod: %s/%s container %s error: %v, existing requested: %.3f, current request: %.3f, total requested: %.3f",
				hint.Nodes, req.PodNamespace, req.PodName, req.ContainerName, checkErr,
				existingRequestedQuantity, req.CPURequest, totalRequestedQuantity)
			scope += fmt.Sprintf("numa%s ", hintNUMASet.String())
			continue
		}
		filteredTopologyHints = append(filteredTopologyHints, hint)
	}

	if len(filteredTopologyHints) == 0 {
		return fmt.Errorf("pod: %s/%s, container: %s shared_cores numa_binding cpu total request exceeds threshold, current request: %.3f, ratio: %.3f, scope: %s: %w",
			req.PodNamespace, req.PodName, req.ContainerName, req.CPURequest, f.cpuTotalRequestThreshold, scope, cpuutil.ErrNoAvailableCPUHints)
	}

	hints.Hints = filteredTopologyHints
	return nil
}

// getSNBCPUTotalRequest sums up SNB+DNB cpu request quantity on the given numa
// set, excluding the requesting pod itself (so that in-place resize does not
// double count its own existing allocation), and adds the new request.
func (f *SNBCPUTotalRequestThresholdFilter) getSNBCPUTotalRequest(
	req *pluginapi.ResourceRequest,
	request float64,
	machineState state.NUMANodeMap,
	numaSet machine.CPUSet,
) float64 {
	existingRequestedQuantity := 0.0
	for _, nodeID := range numaSet.ToSliceNoSortInt() {
		ns := machineState[nodeID]
		if ns == nil {
			continue
		}
		existingRequestedQuantity += state.GetRequestedQuantityFromPodEntries(ns.PodEntries,
			func(ai *state.AllocationInfo) bool {
				if ai == nil || ai.PodUid == req.PodUid {
					return false
				}
				return ai.CheckSharedOrDedicatedNUMABinding()
			}, f.getContainerRequestedCores)
	}
	return existingRequestedQuantity + request
}

func (f *SNBCPUTotalRequestThresholdFilter) checkThreshold(
	req *pluginapi.ResourceRequest,
	totalRequestedQuantity float64,
	totalAllocatable float64,
	scope string,
) error {
	if req == nil {
		return fmt.Errorf("got nil request")
	}
	if totalAllocatable <= 0 {
		return fmt.Errorf("pod: %s/%s, container: %s got non-positive shared_cores numa_binding cpu total request threshold total allocatable: %.3f, scope: %s",
			req.PodNamespace, req.PodName, req.ContainerName, totalAllocatable, scope)
	}
	allowed := totalAllocatable * f.cpuTotalRequestThreshold
	if !cpuutil.CPUIsSufficient(totalRequestedQuantity, allowed) {
		return fmt.Errorf("pod: %s/%s, container: %s shared_cores numa_binding cpu total request %.3f exceeds threshold %.3f, total allocatable: %.3f, ratio: %.3f, scope: %s",
			req.PodNamespace, req.PodName, req.ContainerName, totalRequestedQuantity, allowed, totalAllocatable, f.cpuTotalRequestThreshold, scope)
	}
	return nil
}
