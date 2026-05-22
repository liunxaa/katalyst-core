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
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type mockState struct {
	machineState state.NUMANodeMap
}

func (m *mockState) GetMachineState() state.NUMANodeMap { return m.machineState }
func (m *mockState) GetNUMAHeadroom() map[int]float64   { return nil }
func (m *mockState) GetPodEntries() state.PodEntries    { return nil }
func (m *mockState) GetAllocationInfo(string, string) *state.AllocationInfo {
	return nil
}
func (m *mockState) GetAllowSharedCoresOverlapReclaimedCores() bool { return false }
func (m *mockState) SetMachineState(state.NUMANodeMap, bool)       {}
func (m *mockState) SetNUMAHeadroom(map[int]float64, bool)         {}
func (m *mockState) SetPodEntries(state.PodEntries, bool)          {}
func (m *mockState) SetAllocationInfo(string, string, *state.AllocationInfo, bool) {
}
func (m *mockState) SetAllowSharedCoresOverlapReclaimedCores(bool, bool) {}
func (m *mockState) Delete(string, string, bool)                         {}
func (m *mockState) ClearState()                                         {}
func (m *mockState) StoreState() error                                    { return nil }

func newTestFilter(t *testing.T, ratio float64) *SNBCPUTotalRequestThresholdFilter {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	extraTopologyInfo, err := machine.GenerateDummyExtraTopology(cpuTopology.NumNUMANodes)
	require.NoError(t, err)

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       cpuTopology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	return New(Options{
		State: &mockState{machineState: make(state.NUMANodeMap)},
		ReservedCPUs: machine.NewCPUSet(),
		MachineInfo:  machineInfo,
		GetContainerRequestedCores: func(ai *state.AllocationInfo) float64 {
			return ai.RequestQuantity
		},
		SNBCPUTotalRequestThresholdRatio: ratio,
	})
}

func newTestReq(qosLevel string, reqCPU float64) *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:         "pod-uid",
		PodNamespace:   "default",
		PodName:        "pod",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): reqCPU,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          qosLevel,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
		},
	}
}

func TestFilterNilRequest(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	err := f.Filter(hintoptimizer.Request{}, &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}},
	})
	require.ErrorContains(t, err, "got nil request")
}

func TestFilterInvalidRatio(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 2)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
	})
	require.ErrorContains(t, err, "invalid")
}

func TestFilterDisabledRatio(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
	}
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, hints)
	require.NoError(t, err)
	require.Len(t, hints.Hints, 1)
}

func TestFilterNilHints(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, nil)
	require.NoError(t, err)
}

func TestFilterEmptyHints(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{},
	})
	require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
}

func TestFilterNilCPUHints(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, nil)
	require.NoError(t, err)
}

func TestFilterNilTopologyHintSkipped(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{nil, {Nodes: []uint64{0}, Preferred: true}},
	}
	err := f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, hints)
	require.NoError(t, err)
	require.Len(t, hints.Hints, 1)
}

func TestCheckThresholdNilRequest(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	err := f.checkThreshold(nil, 1, 4, "numa:0")
	require.ErrorContains(t, err, "got nil request")
}

func TestCheckThresholdNonPositiveAllocatable(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.checkThreshold(req, 1, -1, "numa 0")
	require.ErrorContains(t, err, "non-positive")
}

func TestCheckThreshold(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)

	numaCPUSet := f.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	allowed := float64(numaCPUSet.Size()) * f.cpuTotalRequestThreshold
	require.NoError(t, f.checkThreshold(req, allowed, float64(numaCPUSet.Size()), "numa:0"))
	require.ErrorContains(t, f.checkThreshold(req, allowed+0.001, float64(numaCPUSet.Size()), "numa:0"), "exceeds threshold")
}

func TestCheckThresholdExceedsThreshold(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	err := f.checkThreshold(req, 100, 4, "numa:0")
	require.ErrorContains(t, err, "exceeds threshold")
}

func TestGetSNBCPUTotalRequest(t *testing.T) {
	t.Parallel()

	// cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	// require.NoError(t, err)

	f := newTestFilter(t, 0.5)

	newAllocation := func(podUID, containerName, containerType, ownerPoolName, qosLevel string,
		numaBinding bool, request float64, extraAnnotations map[string]string,
	) *state.AllocationInfo {
		annotations := map[string]string{
			consts.PodAnnotationQoSLevelKey: qosLevel,
		}
		if numaBinding {
			annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
		}
		for k, v := range extraAnnotations {
			annotations[k] = v
		}
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podUID,
				ContainerName: containerName,
				ContainerType: containerType,
				OwnerPoolName: ownerPoolName,
				Annotations:   annotations,
				QoSLevel:      qosLevel,
			},
			RequestQuantity: request,
		}
	}

	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   machine.NewCPUSet(0, 1, 2, 3),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"snb-pod": state.ContainerEntries{
					"main": newAllocation("snb-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 1.25, nil),
					"sidecar": newAllocation("snb-pod", "sidecar", pluginapi.ContainerType_SIDECAR.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 0.5, nil),
					"nil": nil,
				},
				"legacy-snb-pod": state.ContainerEntries{
					"main": newAllocation("legacy-snb-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 1.5, nil),
				},
				"non-binding-pod": state.ContainerEntries{
					"main": newAllocation("non-binding-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, false, 10, nil),
				},
				"dedicated-pod": state.ContainerEntries{
					"main": newAllocation("dedicated-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameDedicated, consts.PodAnnotationQoSLevelDedicatedCores, true, 4, nil),
				},
				"reclaimed-pod": state.ContainerEntries{
					"main": newAllocation("reclaimed-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameReclaim, consts.PodAnnotationQoSLevelReclaimedCores, true, 2, nil),
				},
				"pod-uid": state.ContainerEntries{
					"main": newAllocation("pod-uid", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 0.4, nil),
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					commonstate.FakedContainerName: newAllocation(commonstate.PoolNameShare, commonstate.FakedContainerName,
						pluginapi.ContainerType_MAIN.String(), commonstate.PoolNameShare,
						consts.PodAnnotationQoSLevelSharedCores, true, 100, nil),
				},
			},
		},
		1: &state.NUMANodeState{
			DefaultCPUSet:   machine.NewCPUSet(4, 5, 6, 7),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"snb-on-numa-1": state.ContainerEntries{
					"main": newAllocation("snb-on-numa-1", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 2, nil),
				},
			},
		},
	}

	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	req.PodUid = "pod-uid"

	require.InDelta(t, 8.25, f.getSNBCPUTotalRequest(req, 1.0, machineState, machine.NewCPUSet(0)), 1e-9)
	require.InDelta(t, 10.25, f.getSNBCPUTotalRequest(req, 1.0, machineState, machine.NewCPUSet(0, 1)), 1e-9)

	reqNew := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	reqNew.PodUid = "another-pod"
	require.InDelta(t, 8.65, f.getSNBCPUTotalRequest(reqNew, 1.0, machineState, machine.NewCPUSet(0)), 1e-9)
}

func TestGetSNBCPUTotalRequestNilNUMANode(t *testing.T) {
	t.Parallel()

	f := newTestFilter(t, 0.5)

	machineState := state.NUMANodeMap{
		0: nil,
		1: &state.NUMANodeState{
			DefaultCPUSet:   machine.NewCPUSet(4, 5, 6, 7),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"snb-pod": state.ContainerEntries{
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "snb-pod",
							PodNamespace:  "default",
							PodName:       "snb-pod",
							ContainerName: "main",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
							OwnerPoolName: commonstate.PoolNameShare,
						},
						RequestQuantity: 2,
					},
				},
			},
		},
	}

	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	require.InDelta(t, 3.0, f.getSNBCPUTotalRequest(req, 1.0, machineState, machine.NewCPUSet(0, 1)), 1e-9)
}

func TestFilterPrunesHints(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	extraTopologyInfo, err := machine.GenerateDummyExtraTopology(cpuTopology.NumNUMANodes)
	require.NoError(t, err)

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       cpuTopology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	numa0CPUs := machineInfo.CPUDetails.CPUsInNUMANodes(0)
	numa1CPUs := machineInfo.CPUDetails.CPUsInNUMANodes(1)

	ratio := 0.5
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   numa0CPUs.Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"existing-snb": state.ContainerEntries{
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "existing-snb",
							PodNamespace:  "default",
							PodName:       "existing-snb",
							ContainerName: "main",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
							OwnerPoolName: commonstate.PoolNameShare,
						},
						RequestQuantity: float64(numa0CPUs.Size()) * ratio,
					},
				},
			},
		},
		1: &state.NUMANodeState{
			DefaultCPUSet:   numa1CPUs.Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
	}

	f := New(Options{
		State:        &mockState{machineState: machineState},
		ReservedCPUs: machine.NewCPUSet(),
		MachineInfo:  machineInfo,
		GetContainerRequestedCores: func(ai *state.AllocationInfo) float64 {
			return ai.RequestQuantity
		},
		SNBCPUTotalRequestThresholdRatio: ratio,
	})

	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{Nodes: []uint64{0}},
			{Nodes: []uint64{1}},
		},
	}

	err = f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, hints)
	require.NoError(t, err)
	require.Len(t, hints.Hints, 1)
	require.Equal(t, []uint64{1}, hints.Hints[0].Nodes)
}

func TestFilterRejectsAllHints(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	extraTopologyInfo, err := machine.GenerateDummyExtraTopology(cpuTopology.NumNUMANodes)
	require.NoError(t, err)

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       cpuTopology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	numa0CPUs := machineInfo.CPUDetails.CPUsInNUMANodes(0)

	ratio := 0.5
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   numa0CPUs.Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"existing-snb": state.ContainerEntries{
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "existing-snb",
							PodNamespace:  "default",
							PodName:       "existing-snb",
							ContainerName: "main",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
							OwnerPoolName: commonstate.PoolNameShare,
						},
						RequestQuantity: float64(numa0CPUs.Size()) * ratio,
					},
				},
			},
		},
	}

	f := New(Options{
		State:        &mockState{machineState: machineState},
		ReservedCPUs: machine.NewCPUSet(),
		MachineInfo:  machineInfo,
		GetContainerRequestedCores: func(ai *state.AllocationInfo) float64 {
			return ai.RequestQuantity
		},
		SNBCPUTotalRequestThresholdRatio: ratio,
	})

	req := newTestReq(consts.PodAnnotationQoSLevelSharedCores, 1)
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{Nodes: []uint64{0}},
		},
	}

	err = f.Filter(hintoptimizer.Request{ResourceRequest: req, CPURequest: 1}, hints)
	require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
}

func TestNewFromFactoryOptions(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	extraTopologyInfo, err := machine.GenerateDummyExtraTopology(cpuTopology.NumNUMANodes)
	require.NoError(t, err)

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       cpuTopology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	filter, err := NewFromFactoryOptions(policy.HintOptimizerFactoryOptions{
		State:        &mockState{machineState: make(state.NUMANodeMap)},
		ReservedCPUs: machine.NewCPUSet(),
		MachineInfo:  machineInfo,
		GetContainerRequestedCores: func(ai *state.AllocationInfo) float64 {
			return ai.RequestQuantity
		},
		SNBCPUTotalRequestThresholdRatio: 0.5,
	})
	require.NoError(t, err)
	require.NotNil(t, filter)
}

func TestRun(t *testing.T) {
	t.Parallel()
	f := newTestFilter(t, 0.5)
	stopCh := make(chan struct{})
	close(stopCh)
	require.NoError(t, f.Run(stopCh))
}
