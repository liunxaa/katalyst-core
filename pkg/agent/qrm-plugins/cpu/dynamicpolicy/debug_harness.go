/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	"fmt"
	"os"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	defaultDebugReservedCPUs = 2
	debugPodAnnotationKey    = "qrm.katalyst.kubewharf.io/debug_pod"
)

// DebugPolicyOptions describes how to bootstrap a standalone DynamicPolicy for
// local QRM flow inspection.
type DebugPolicyOptions struct {
	StateFileDirectory                    string
	ReservedCPUs                          *int
	ReservedReclaimedCPUsSize             *int
	InitReservePool                       bool
	InitReclaimPool                       bool
	EnableReclaim                         bool
	EnableReclaimNUMABinding              bool
	AllowSharedCoresOverlapReclaimedCores bool
	SNBCPUThresholdRatio                  float64
}

// DebugSnapshot is a read-only copy of the parts of DynamicPolicy state that
// explain hint and allocation decisions.
type DebugSnapshot struct {
	ReservedCPUs                          machine.CPUSet        `json:"reserved_cpus"`
	ReservedReclaimedCPUsSize             int                   `json:"reserved_reclaimed_cpus_size"`
	EnableReclaim                         bool                  `json:"enable_reclaim"`
	AllowSharedCoresOverlapReclaimedCores bool                  `json:"allow_shared_cores_overlap_reclaimed_cores"`
	ReclaimOverlapShareRatio              map[string]float64    `json:"reclaim_overlap_share_ratio,omitempty"`
	ReclaimOverlapShareRatioError         string                `json:"reclaim_overlap_share_ratio_error,omitempty"`
	MachineState                          state.NUMANodeMap     `json:"machine_state"`
	NUMAHeadroom                          map[int]float64       `json:"numa_headroom,omitempty"`
	PodEntries                            state.PodEntries      `json:"pod_entries"`
	AllocationInfo                        *state.AllocationInfo `json:"allocation_info,omitempty"`
}

// NewDebugDynamicPolicy creates a DynamicPolicy with dummy topology metadata
// and checkpoint state. It intentionally reuses the same handlers as the real
// CPU QRM plugin, so callers can exercise GetTopologyHints and Allocate without
// starting kubelet or katalyst-agent.
func NewDebugDynamicPolicy(topology *machine.CPUTopology, opts DebugPolicyOptions) (*DynamicPolicy, string, error) {
	if topology == nil {
		return nil, "", fmt.Errorf("topology is nil")
	}

	stateDir := opts.StateFileDirectory
	if stateDir == "" {
		tmpDir, err := os.MkdirTemp("", "qrm-debugger-cpu-state-")
		if err != nil {
			return nil, "", fmt.Errorf("create temp state dir failed: %w", err)
		}
		stateDir = tmpDir
	}

	reservedCPUsCount := defaultDebugReservedCPUs
	if opts.ReservedCPUs != nil {
		reservedCPUsCount = *opts.ReservedCPUs
	}
	if reservedCPUsCount < 0 {
		return nil, "", fmt.Errorf("reserved cpus must be non-negative")
	}
	if reservedCPUsCount > topology.NumCPUs {
		return nil, "", fmt.Errorf("reserved cpus %d exceeds topology cpus %d", reservedCPUsCount, topology.NumCPUs)
	}

	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: stateDir,
	}
	stateImpl, err := state.NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameDynamic, topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	if err != nil {
		return nil, "", fmt.Errorf("NewCheckpointState failed: %w", err)
	}

	extraTopologyInfo, err := machine.GenerateDummyExtraTopology(topology.NumNUMANodes)
	if err != nil {
		return nil, "", fmt.Errorf("GenerateDummyExtraTopology failed: %w", err)
	}
	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       topology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	reservedCPUs := machine.NewCPUSet()
	if reservedCPUsCount > 0 {
		reservedCPUs, _, err = calculator.TakeHTByNUMABalance(machineInfo, machineInfo.CPUDetails.CPUs().Clone(), reservedCPUsCount)
		if err != nil {
			return nil, "", fmt.Errorf("calculate reserved cpus failed: %w", err)
		}
	}

	conf, err := options.NewOptions().Config()
	if err != nil {
		return nil, "", fmt.Errorf("build default agent config failed: %w", err)
	}
	conf.StateFileDirectory = stateDir

	dynamicConfig := dynamic.NewDynamicAgentConfiguration()
	dynamicConfig.GetDynamicConfiguration().EnableReclaim = opts.EnableReclaim

	reservedReclaimedSize := general.Max(reservedReclaimedCPUsSize, topology.NumNUMANodes)
	if opts.ReservedReclaimedCPUsSize != nil {
		if *opts.ReservedReclaimedCPUsSize < 0 {
			return nil, "", fmt.Errorf("reserved reclaimed cpus size must be non-negative")
		}
		reservedReclaimedSize = *opts.ReservedReclaimedCPUsSize
	}

	policyImplement := &DynamicPolicy{
		conf:                      conf,
		machineInfo:               machineInfo,
		qosConfig:                 generic.NewQoSConfiguration(),
		dynamicConfig:             dynamicConfig,
		state:                     stateImpl,
		advisorValidator:          validator.NewCPUAdvisorValidator(stateImpl, machineInfo),
		featureGateManager:        featuregatenegotiation.NewFeatureGateManager(config.NewConfiguration()),
		reservedReclaimedCPUsSize: reservedReclaimedSize,
		reservedCPUs:              reservedCPUs,
		enableReclaimNUMABinding:  opts.EnableReclaimNUMABinding,
		snbCPUThresholdRatio:      opts.SNBCPUThresholdRatio,
		emitter:                   metrics.DummyMetrics{},
		podDebugAnnoKeys:          []string{debugPodAnnotationKey},
		numaNumberAnnotationKey:   consts.PodAnnotationCPUEnhancementNumaNumber,
		numaIDsAnnotationKey:      consts.PodAnnotationCPUEnhancementNumaIDs,
	}

	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}
	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	policyImplement.metaServer = &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			PodFetcher:          &pod.PodFetcherStub{},
			KatalystMachineInfo: machineInfo,
			MetricsFetcher:      metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}

	if err = policyImplement.initHintOptimizers(); err != nil {
		return nil, "", fmt.Errorf("initHintOptimizers failed: %w", err)
	}
	if opts.AllowSharedCoresOverlapReclaimedCores {
		policyImplement.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	}
	if opts.InitReservePool {
		if err = policyImplement.initReservePool(); err != nil {
			return nil, "", fmt.Errorf("initReservePool failed: %w", err)
		}
	}
	if opts.InitReclaimPool {
		if err = policyImplement.initReclaimPool(); err != nil {
			return nil, "", fmt.Errorf("initReclaimPool failed: %w", err)
		}
	}

	return policyImplement, stateDir, nil
}

// DebugSnapshot returns a clone of the current DynamicPolicy state.
func (p *DynamicPolicy) DebugSnapshot(podUID, containerName string) *DebugSnapshot {
	p.RLock()
	defer p.RUnlock()

	numaHeadroom := p.state.GetNUMAHeadroom()
	clonedHeadroom := make(map[int]float64, len(numaHeadroom))
	for numaID, quantity := range numaHeadroom {
		clonedHeadroom[numaID] = quantity
	}

	podEntries := p.state.GetPodEntries()
	reclaimOverlapShareRatio, reclaimOverlapErr := p.getReclaimOverlapShareRatio(podEntries)
	reclaimOverlapErrMsg := ""
	if reclaimOverlapErr != nil {
		reclaimOverlapErrMsg = reclaimOverlapErr.Error()
	}

	return &DebugSnapshot{
		ReservedCPUs:                          p.reservedCPUs.Clone(),
		ReservedReclaimedCPUsSize:             p.reservedReclaimedCPUsSize,
		EnableReclaim:                         p.dynamicConfig.GetDynamicConfiguration().EnableReclaim,
		AllowSharedCoresOverlapReclaimedCores: p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		ReclaimOverlapShareRatio:              reclaimOverlapShareRatio,
		ReclaimOverlapShareRatioError:         reclaimOverlapErrMsg,
		MachineState:                          p.state.GetMachineState().Clone(),
		NUMAHeadroom:                          clonedHeadroom,
		PodEntries:                            podEntries.Clone(),
		AllocationInfo:                        p.state.GetAllocationInfo(podUID, containerName).Clone(),
	}
}

// DebugHandlerNames returns the concrete hint and allocation handlers that the
// request will reach after DynamicPolicy dispatches by QoS and annotations.
func (p *DynamicPolicy) DebugHandlerNames(reqAnnotations map[string]string, qosLevel, containerType string) (string, string) {
	if containerType == "sidecar" {
		switch qosLevel {
		case consts.PodAnnotationQoSLevelSharedCores:
			return "sharedCoresHintHandler -> sharedCoresWithNUMABindingHintHandler (sidecar no-preference path)",
				"sharedCoresAllocationHandler -> sharedCoresWithNUMABindingAllocationHandler -> allocationSidecarHandler"
		case consts.PodAnnotationQoSLevelDedicatedCores:
			return "dedicatedCoresHintHandler -> dedicatedCoresWithNUMABindingHintHandler (sidecar no-preference path)",
				"dedicatedCoresAllocationHandler -> dedicatedCoresWithNUMABindingAllocationHandler -> allocationSidecarHandler"
		}
	}

	numaBinding := reqAnnotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	switch qosLevel {
	case consts.PodAnnotationQoSLevelSharedCores:
		if numaBinding {
			return "sharedCoresHintHandler -> sharedCoresWithNUMABindingHintHandler -> calculateHintsForNUMABindingSharedCores -> filterHintsBySNBCPUThreshold",
				"sharedCoresAllocationHandler -> sharedCoresWithNUMABindingAllocationHandler -> allocateSharedNumaBindingCPUs -> doAndCheckPutAllocationInfo"
		}
		return "sharedCoresHintHandler -> checkNonBindingShareCoresCpuResource -> PackResourceHintsResponse(no NUMA preference)",
			"sharedCoresAllocationHandler -> sharedCoresWithoutNUMABindingAllocationHandler -> putAllocationsAndAdjustAllocationEntries"
	case consts.PodAnnotationQoSLevelDedicatedCores:
		if numaBinding {
			return "dedicatedCoresHintHandler -> dedicatedCoresWithNUMABindingHintHandler",
				"dedicatedCoresAllocationHandler -> dedicatedCoresWithNUMABindingAllocationHandler -> allocateNumaBindingCPUs -> doAndCheckPutAllocationInfo"
		}
		return "dedicatedCoresHintHandler -> dedicatedCoresWithoutNUMABindingHintHandler",
			"dedicatedCoresAllocationHandler -> dedicatedCoresWithoutNUMABindingAllocationHandler"
	case consts.PodAnnotationQoSLevelReclaimedCores:
		if numaBinding && p.enableReclaimNUMABinding {
			return "reclaimedCoresHintHandler -> reclaimedCoresWithNUMABindingHintHandler -> calculateHintsForNUMABindingReclaimedCores",
				"reclaimedCoresAllocationHandler -> reclaimedCoresWithNUMABindingAllocationHandler"
		}
		return "reclaimedCoresHintHandler -> PackResourceHintsResponse(no NUMA preference)",
			"reclaimedCoresAllocationHandler"
	case consts.PodAnnotationQoSLevelSystemCores:
		return "systemCoresHintHandler", "systemCoresAllocationHandler"
	default:
		return "unsupported QoS hint handler", "unsupported QoS allocation handler"
	}
}
