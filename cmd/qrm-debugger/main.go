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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	formatMarkdown = "markdown"
	formatJSON     = "json"

	hintSelectionFirstPreferred = "first-preferred"
	hintSelectionFirst          = "first"
	hintSelectionNone           = "none"
)

type scenario struct {
	Topology                              topologySpec `json:"topology"`
	StateDirectory                        string       `json:"stateDirectory,omitempty"`
	ReservedCPUs                          *int         `json:"reservedCPUs,omitempty"`
	ReservedReclaimedCPUsSize             *int         `json:"reservedReclaimedCPUsSize,omitempty"`
	InitReservePool                       *bool        `json:"initReservePool,omitempty"`
	InitReclaimPool                       *bool        `json:"initReclaimPool,omitempty"`
	EnableReclaim                         *bool        `json:"enableReclaim,omitempty"`
	EnableReclaimNUMABinding              *bool        `json:"enableReclaimNUMABinding,omitempty"`
	AllowSharedCoresOverlapReclaimedCores *bool        `json:"allowSharedCoresOverlapReclaimedCores,omitempty"`
	SNBCPUThresholdRatio                  float64      `json:"snbCPUThresholdRatio,omitempty"`
	HintSelection                         string       `json:"hintSelection,omitempty"`
	Pods                                  []podSpec    `json:"pods"`
}

type topologySpec struct {
	CPUs    int `json:"cpus"`
	Sockets int `json:"sockets"`
	NUMAs   int `json:"numas"`
}

type podSpec struct {
	UID         string            `json:"uid,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Name        string            `json:"name"`
	QoS         string            `json:"qos,omitempty"`
	NUMABinding bool              `json:"numaBinding,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Containers  []containerSpec   `json:"containers"`
}

type containerSpec struct {
	Name          string            `json:"name"`
	CPU           float64           `json:"cpu"`
	QoS           string            `json:"qos,omitempty"`
	NUMABinding   *bool             `json:"numaBinding,omitempty"`
	ContainerType string            `json:"containerType,omitempty"`
	HintNodes     []uint64          `json:"hintNodes,omitempty"`
	SkipAllocate  bool              `json:"skipAllocate,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type trace struct {
	Scenario       scenario                     `json:"scenario"`
	StateDirectory string                       `json:"stateDirectory"`
	Steps          []traceStep                  `json:"steps"`
	FinalSnapshot  *dynamicpolicy.DebugSnapshot `json:"finalSnapshot,omitempty"`
	Mermaid        string                       `json:"mermaid"`
}

type traceStep struct {
	Index               int                                   `json:"index"`
	PodUID              string                                `json:"podUID"`
	PodNamespace        string                                `json:"podNamespace"`
	PodName             string                                `json:"podName"`
	ContainerName       string                                `json:"containerName"`
	QoS                 string                                `json:"qos"`
	NUMABinding         bool                                  `json:"numaBinding"`
	CPURequest          float64                               `json:"cpuRequest"`
	HintHandler         string                                `json:"hintHandler"`
	AllocationHandler   string                                `json:"allocationHandler"`
	GetTopologyHints    *pluginapi.ResourceHintsResponse      `json:"getTopologyHints,omitempty"`
	GetTopologyHintsErr string                                `json:"getTopologyHintsError,omitempty"`
	SelectedHint        *pluginapi.TopologyHint               `json:"selectedHint,omitempty"`
	Allocate            *pluginapi.ResourceAllocationResponse `json:"allocate,omitempty"`
	AllocateErr         string                                `json:"allocateError,omitempty"`
	Before              *dynamicpolicy.DebugSnapshot          `json:"before,omitempty"`
	AfterHints          *dynamicpolicy.DebugSnapshot          `json:"afterHints,omitempty"`
	AfterAllocate       *dynamicpolicy.DebugSnapshot          `json:"afterAllocate,omitempty"`
}

func main() {
	scenarioPath := flag.String("scenario", "", "Path to a QRM debugger scenario JSON file, or '-' for stdin.")
	outputFormat := flag.String("format", formatMarkdown, "Output format: markdown or json.")
	outputPath := flag.String("out", "", "Optional output path. Defaults to stdout.")
	printSample := flag.Bool("print-sample", false, "Print a sample scenario JSON and exit.")
	webAddr := flag.String("web", "", "Start interactive web UI on address, for example 127.0.0.1:18080.")
	flag.Parse()

	if *printSample {
		writeOutput(*outputPath, []byte(sampleScenario()))
		return
	}
	if *webAddr != "" {
		if err := serveWebUI(*webAddr); err != nil {
			exitf("%v", err)
		}
		return
	}
	if *scenarioPath == "" {
		exitf("missing -scenario; use -print-sample to generate a starting scenario, or -web to start the UI")
	}

	sc, err := readScenario(*scenarioPath)
	if err != nil {
		exitf("%v", err)
	}

	result, err := runScenario(context.Background(), sc)
	if err != nil {
		exitf("%v", err)
	}

	var out []byte
	switch *outputFormat {
	case formatJSON:
		out, err = json.MarshalIndent(result, "", "  ")
	case formatMarkdown:
		out, err = renderMarkdown(result)
	default:
		err = fmt.Errorf("unsupported -format %q", *outputFormat)
	}
	if err != nil {
		exitf("%v", err)
	}
	writeOutput(*outputPath, append(out, '\n'))
}

func readScenario(path string) (scenario, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return scenario{}, fmt.Errorf("read scenario failed: %w", err)
	}

	var sc scenario
	if err = json.Unmarshal(data, &sc); err != nil {
		return scenario{}, fmt.Errorf("parse scenario failed: %w", err)
	}
	if err = defaultAndValidateScenario(&sc); err != nil {
		return scenario{}, err
	}
	return sc, nil
}

func defaultAndValidateScenario(sc *scenario) error {
	if sc.Topology.CPUs == 0 {
		sc.Topology.CPUs = 16
	}
	if sc.Topology.Sockets == 0 {
		sc.Topology.Sockets = 2
	}
	if sc.Topology.NUMAs == 0 {
		sc.Topology.NUMAs = 4
	}
	if sc.HintSelection == "" {
		sc.HintSelection = hintSelectionFirstPreferred
	}
	switch sc.HintSelection {
	case hintSelectionFirstPreferred, hintSelectionFirst, hintSelectionNone:
	default:
		return fmt.Errorf("unsupported hintSelection %q", sc.HintSelection)
	}
	if sc.InitReservePool == nil {
		enabled := true
		sc.InitReservePool = &enabled
	}
	if sc.ReservedReclaimedCPUsSize != nil && *sc.ReservedReclaimedCPUsSize < 0 {
		return fmt.Errorf("reservedReclaimedCPUsSize must be non-negative")
	}
	if sc.InitReclaimPool == nil {
		enabled := true
		sc.InitReclaimPool = &enabled
	}
	if sc.EnableReclaim == nil {
		disabled := false
		sc.EnableReclaim = &disabled
	}
	if sc.EnableReclaimNUMABinding == nil {
		enabled := true
		sc.EnableReclaimNUMABinding = &enabled
	}
	if sc.AllowSharedCoresOverlapReclaimedCores == nil {
		disabled := false
		sc.AllowSharedCoresOverlapReclaimedCores = &disabled
	}

	if len(sc.Pods) == 0 {
		return fmt.Errorf("scenario must include at least one pod")
	}
	for podIndex := range sc.Pods {
		pod := &sc.Pods[podIndex]
		if pod.Name == "" {
			return fmt.Errorf("pods[%d].name is required", podIndex)
		}
		if pod.Namespace == "" {
			pod.Namespace = "default"
		}
		if pod.UID == "" {
			pod.UID = pod.Namespace + "/" + pod.Name
		}
		if pod.QoS == "" {
			pod.QoS = consts.PodAnnotationQoSLevelSharedCores
		}
		if len(pod.Containers) == 0 {
			return fmt.Errorf("pod %s/%s must include at least one container", pod.Namespace, pod.Name)
		}
		for containerIndex := range pod.Containers {
			container := &pod.Containers[containerIndex]
			if container.Name == "" {
				return fmt.Errorf("pod %s/%s containers[%d].name is required", pod.Namespace, pod.Name, containerIndex)
			}
			if container.CPU < 0 {
				return fmt.Errorf("pod %s/%s container %s cpu must be non-negative", pod.Namespace, pod.Name, container.Name)
			}
			if container.QoS == "" {
				container.QoS = pod.QoS
			}
			if container.ContainerType == "" {
				container.ContainerType = "main"
			}
		}
	}
	return nil
}

func runScenario(ctx context.Context, sc scenario) (*trace, error) {
	if err := defaultAndValidateScenario(&sc); err != nil {
		return nil, err
	}

	topology, err := machine.GenerateDummyCPUTopology(sc.Topology.CPUs, sc.Topology.Sockets, sc.Topology.NUMAs)
	if err != nil {
		return nil, fmt.Errorf("generate cpu topology failed: %w", err)
	}

	policy, stateDir, err := dynamicpolicy.NewDebugDynamicPolicy(topology, dynamicpolicy.DebugPolicyOptions{
		StateFileDirectory:                    sc.StateDirectory,
		ReservedCPUs:                          sc.ReservedCPUs,
		ReservedReclaimedCPUsSize:             sc.ReservedReclaimedCPUsSize,
		InitReservePool:                       *sc.InitReservePool,
		InitReclaimPool:                       *sc.InitReclaimPool,
		EnableReclaim:                         *sc.EnableReclaim,
		EnableReclaimNUMABinding:              *sc.EnableReclaimNUMABinding,
		AllowSharedCoresOverlapReclaimedCores: *sc.AllowSharedCoresOverlapReclaimedCores,
		SNBCPUThresholdRatio:                  sc.SNBCPUThresholdRatio,
	})
	if err != nil {
		return nil, err
	}

	result := &trace{
		Scenario:       sc,
		StateDirectory: stateDir,
		Steps:          make([]traceStep, 0),
	}

	stepIndex := 0
	for _, pod := range sc.Pods {
		for containerIndex, container := range pod.Containers {
			req := buildResourceRequest(pod, container, uint64(containerIndex))
			hintHandler, allocationHandler := policy.DebugHandlerNames(req.Annotations, container.QoS, strings.ToLower(container.ContainerType))

			step := traceStep{
				Index:             stepIndex,
				PodUID:            req.PodUid,
				PodNamespace:      req.PodNamespace,
				PodName:           req.PodName,
				ContainerName:     req.ContainerName,
				QoS:               container.QoS,
				NUMABinding:       req.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				CPURequest:        container.CPU,
				HintHandler:       hintHandler,
				AllocationHandler: allocationHandler,
				Before:            policy.DebugSnapshot(req.PodUid, req.ContainerName),
			}

			hintResp, hintErr := policy.GetTopologyHints(ctx, cloneResourceRequest(req))
			step.GetTopologyHints = hintResp
			if hintErr != nil {
				step.GetTopologyHintsErr = hintErr.Error()
			}
			step.AfterHints = policy.DebugSnapshot(req.PodUid, req.ContainerName)

			selectedHint, selectErr := chooseHint(hintResp, container.HintNodes, sc.HintSelection)
			if selectErr != nil {
				if step.GetTopologyHintsErr == "" {
					step.GetTopologyHintsErr = selectErr.Error()
				}
			}
			step.SelectedHint = selectedHint

			if !container.SkipAllocate && hintErr == nil && selectErr == nil {
				allocateReq := cloneResourceRequest(req)
				allocateReq.Hint = selectedHint
				allocateResp, allocateErr := policy.Allocate(ctx, allocateReq)
				step.Allocate = allocateResp
				if allocateErr != nil {
					step.AllocateErr = allocateErr.Error()
				}
			}
			step.AfterAllocate = policy.DebugSnapshot(req.PodUid, req.ContainerName)

			result.Steps = append(result.Steps, step)
			stepIndex++
		}
	}

	result.FinalSnapshot = policy.DebugSnapshot("", "")
	result.Mermaid = renderMermaid(result)
	return result, nil
}

func buildResourceRequest(pod podSpec, container containerSpec, containerIndex uint64) *pluginapi.ResourceRequest {
	labels := mergeStringMaps(pod.Labels, container.Labels)
	annotations := mergeStringMaps(pod.Annotations, container.Annotations)

	qos := container.QoS
	annotations[consts.PodAnnotationQoSLevelKey] = qos

	numaBinding := pod.NUMABinding
	if container.NUMABinding != nil {
		numaBinding = *container.NUMABinding
	}
	if numaBinding {
		annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
		if _, ok := annotations[consts.PodAnnotationMemoryEnhancementKey]; !ok {
			annotations[consts.PodAnnotationMemoryEnhancementKey] = `{"numa_binding":"true"}`
		}
	}

	return &pluginapi.ResourceRequest{
		PodUid:         pod.UID,
		PodNamespace:   pod.Namespace,
		PodName:        pod.Name,
		ContainerName:  container.Name,
		ContainerType:  parseContainerType(container.ContainerType),
		ContainerIndex: containerIndex,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): container.CPU,
		},
		Labels:      labels,
		Annotations: annotations,
	}
}

func parseContainerType(containerType string) pluginapi.ContainerType {
	switch strings.ToLower(containerType) {
	case "init":
		return pluginapi.ContainerType_INIT
	case "sidecar":
		return pluginapi.ContainerType_SIDECAR
	default:
		return pluginapi.ContainerType_MAIN
	}
}

func chooseHint(resp *pluginapi.ResourceHintsResponse, explicitNodes []uint64, selection string) (*pluginapi.TopologyHint, error) {
	if len(explicitNodes) > 0 {
		return &pluginapi.TopologyHint{Nodes: append([]uint64(nil), explicitNodes...), Preferred: true}, nil
	}
	if selection == hintSelectionNone {
		return nil, nil
	}
	if resp == nil || resp.ResourceHints == nil {
		return nil, nil
	}

	cpuHints := resp.ResourceHints[string(v1.ResourceCPU)]
	if cpuHints == nil {
		return nil, nil
	}
	if len(cpuHints.Hints) == 0 {
		return nil, fmt.Errorf("cpu hints are empty")
	}

	if selection == hintSelectionFirstPreferred {
		for _, hint := range cpuHints.Hints {
			if hint != nil && hint.Preferred {
				return cloneHint(hint), nil
			}
		}
	}
	return cloneHint(cpuHints.Hints[0]), nil
}

func cloneResourceRequest(req *pluginapi.ResourceRequest) *pluginapi.ResourceRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.ResourceRequests = copyFloatMap(req.ResourceRequests)
	cloned.Labels = copyStringMap(req.Labels)
	cloned.Annotations = copyStringMap(req.Annotations)
	cloned.Hint = cloneHint(req.Hint)
	return &cloned
}

func cloneHint(hint *pluginapi.TopologyHint) *pluginapi.TopologyHint {
	if hint == nil {
		return nil
	}
	return &pluginapi.TopologyHint{
		Nodes:     append([]uint64(nil), hint.Nodes...),
		Preferred: hint.Preferred,
	}
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, item := range maps {
		for key, value := range item {
			merged[key] = value
		}
	}
	return merged
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func copyFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func renderMarkdown(result *trace) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# QRM CPU Dynamic Policy Trace\n\n")
	fmt.Fprintf(&buf, "- state directory: `%s`\n", result.StateDirectory)
	fmt.Fprintf(&buf, "- topology: %d CPUs, %d sockets, %d NUMA nodes\n", result.Scenario.Topology.CPUs, result.Scenario.Topology.Sockets, result.Scenario.Topology.NUMAs)
	fmt.Fprintf(&buf, "- hint selection: `%s`\n\n", result.Scenario.HintSelection)

	fmt.Fprintf(&buf, "## Flow\n\n")
	fmt.Fprintf(&buf, "```mermaid\n%s\n```\n\n", result.Mermaid)

	for _, step := range result.Steps {
		fmt.Fprintf(&buf, "## Step %d: %s/%s %s\n\n", step.Index, step.PodNamespace, step.PodName, step.ContainerName)
		fmt.Fprintf(&buf, "- qos: `%s`\n", step.QoS)
		fmt.Fprintf(&buf, "- numa binding: `%t`\n", step.NUMABinding)
		fmt.Fprintf(&buf, "- cpu request: `%.3f`\n", step.CPURequest)
		fmt.Fprintf(&buf, "- hint chain: `%s`\n", step.HintHandler)
		fmt.Fprintf(&buf, "- allocation chain: `%s`\n", step.AllocationHandler)
		if step.GetTopologyHintsErr != "" {
			fmt.Fprintf(&buf, "- GetTopologyHints error: `%s`\n", step.GetTopologyHintsErr)
		}
		if step.AllocateErr != "" {
			fmt.Fprintf(&buf, "- Allocate error: `%s`\n", step.AllocateErr)
		}
		fmt.Fprintf(&buf, "\n")

		writeJSONBlock(&buf, "selected hint", step.SelectedHint)
		writeJSONBlock(&buf, "hints response", step.GetTopologyHints)
		writeJSONBlock(&buf, "allocation response", step.Allocate)
		writeJSONBlock(&buf, "allocation info before", allocationInfo(step.Before))
		writeJSONBlock(&buf, "allocation info after allocate", allocationInfo(step.AfterAllocate))
		writeJSONBlock(&buf, "machine state after allocate", machineState(step.AfterAllocate))
	}

	fmt.Fprintf(&buf, "## Final PodEntries\n\n")
	writeJSONBlock(&buf, "pod entries", result.FinalSnapshot.PodEntries)
	return buf.Bytes(), nil
}

func renderMermaid(result *trace) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "flowchart TD\n")
	fmt.Fprintf(&buf, "  start([scenario start])\n")
	previous := "start"
	for _, step := range result.Steps {
		hintID := fmt.Sprintf("hint_%d", step.Index)
		allocID := fmt.Sprintf("alloc_%d", step.Index)
		label := mermaidLabel(fmt.Sprintf("%s/%s\\n%s\\nCPU %.3f", step.PodNamespace, step.PodName, step.ContainerName, step.CPURequest))
		hintLabel := mermaidLabel("GetTopologyHints\\n" + step.HintHandler)
		allocLabel := mermaidLabel("Allocate\\n" + step.AllocationHandler)
		fmt.Fprintf(&buf, "  %s --> pod_%d[\"%s\"]\n", previous, step.Index, label)
		fmt.Fprintf(&buf, "  pod_%d --> %s[\"%s\"]\n", step.Index, hintID, hintLabel)
		if step.Allocate != nil || step.AllocateErr != "" {
			fmt.Fprintf(&buf, "  %s --> %s[\"%s\"]\n", hintID, allocID, allocLabel)
			previous = allocID
		} else {
			previous = hintID
		}
	}
	fmt.Fprintf(&buf, "  %s --> done([scenario done])", previous)
	return buf.String()
}

func mermaidLabel(input string) string {
	replacer := strings.NewReplacer("\"", "'", "`", "'", "|", "/", "\n", "\\n")
	return replacer.Replace(input)
}

func writeJSONBlock(buf *bytes.Buffer, title string, value interface{}) {
	if isNil(value) {
		return
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(buf, "### %s\n\n`marshal failed: %v`\n\n", title, err)
		return
	}
	fmt.Fprintf(buf, "### %s\n\n```json\n%s\n```\n\n", title, data)
}

func isNil(value interface{}) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case *pluginapi.TopologyHint:
		return typed == nil
	case *pluginapi.ResourceHintsResponse:
		return typed == nil
	case *pluginapi.ResourceAllocationResponse:
		return typed == nil
	case *dynamicpolicy.DebugSnapshot:
		return typed == nil
	}
	return false
}

func allocationInfo(snapshot *dynamicpolicy.DebugSnapshot) interface{} {
	if snapshot == nil {
		return nil
	}
	return snapshot.AllocationInfo
}

func machineState(snapshot *dynamicpolicy.DebugSnapshot) interface{} {
	if snapshot == nil {
		return nil
	}
	return snapshot.MachineState
}

func writeOutput(path string, data []byte) {
	var err error
	if path == "" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		exitf("write output failed: %v", err)
	}
}

func exitf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func sampleScenario() string {
	enabled := true
	disabled := false
	reservedReclaimedCPUsSize := 4
	sc := scenario{
		Topology:                              topologySpec{CPUs: 16, Sockets: 2, NUMAs: 4},
		ReservedReclaimedCPUsSize:             &reservedReclaimedCPUsSize,
		InitReservePool:                       &enabled,
		InitReclaimPool:                       &enabled,
		EnableReclaim:                         &disabled,
		EnableReclaimNUMABinding:              &enabled,
		AllowSharedCoresOverlapReclaimedCores: &disabled,
		HintSelection:                         hintSelectionFirstPreferred,
		Pods: []podSpec{
			{
				UID:         "pod-snb-1",
				Namespace:   "default",
				Name:        "pod-snb-1",
				QoS:         consts.PodAnnotationQoSLevelSharedCores,
				NUMABinding: true,
				Containers: []containerSpec{
					{Name: "main", CPU: 2},
				},
			},
			{
				UID:       "pod-shared-1",
				Namespace: "default",
				Name:      "pod-shared-1",
				QoS:       consts.PodAnnotationQoSLevelSharedCores,
				Containers: []containerSpec{
					{Name: "main", CPU: 1},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(sc, "", "  ")
	return string(data) + "\n"
}
