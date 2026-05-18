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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestBuildResourceRequestAddsNUMABindingEnhancement(t *testing.T) {
	req := buildResourceRequest(podSpec{
		UID:         "pod-1",
		Namespace:   "default",
		Name:        "pod-1",
		QoS:         consts.PodAnnotationQoSLevelSharedCores,
		NUMABinding: true,
		Containers: []containerSpec{
			{Name: "main", CPU: 2},
		},
	}, containerSpec{
		Name: "main",
		CPU:  2,
		QoS:  consts.PodAnnotationQoSLevelSharedCores,
	}, 0)

	if req.ResourceName != string(v1.ResourceCPU) {
		t.Fatalf("unexpected resource name: %s", req.ResourceName)
	}
	if req.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] != consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
		t.Fatalf("numa binding annotation is not set: %#v", req.Annotations)
	}
	if req.Annotations[consts.PodAnnotationMemoryEnhancementKey] == "" {
		t.Fatalf("memory enhancement annotation is not set: %#v", req.Annotations)
	}
}

func TestHandleRunScenario(t *testing.T) {
	body := strings.NewReader(`{
	  "topology": {"cpus": 16, "sockets": 2, "numas": 4},
	  "pods": [{
	    "uid": "pod-web-1",
	    "namespace": "default",
	    "name": "pod-web-1",
	    "qos": "shared_cores",
	    "numaBinding": true,
	    "containers": [{"name": "main", "cpu": 2}]
	  }]
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()
	handleRunScenario(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"steps"`) {
		t.Fatalf("response does not include trace steps: %s", rec.Body.String())
	}
}

func TestChooseHintFirstPreferred(t *testing.T) {
	hint, err := chooseHint(&pluginapi.ResourceHintsResponse{
		ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): {
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}},
					{Nodes: []uint64{1}, Preferred: true},
				},
			},
		},
	}, nil, hintSelectionFirstPreferred)
	if err != nil {
		t.Fatalf("chooseHint failed: %v", err)
	}
	if hint == nil || len(hint.Nodes) != 1 || hint.Nodes[0] != 1 {
		t.Fatalf("unexpected hint: %#v", hint)
	}
}

func TestRunScenarioSharedNUMABinding(t *testing.T) {
	enabled := true
	sc := scenario{
		Topology:                 topologySpec{CPUs: 16, Sockets: 2, NUMAs: 4},
		InitReservePool:          &enabled,
		InitReclaimPool:          &enabled,
		EnableReclaimNUMABinding: &enabled,
		HintSelection:            hintSelectionFirstPreferred,
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
		},
	}

	result, err := runScenario(context.Background(), sc)
	if err != nil {
		t.Fatalf("runScenario failed: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("unexpected step count: %d", len(result.Steps))
	}
	allocationInfo := result.Steps[0].AfterAllocate.AllocationInfo
	if allocationInfo == nil {
		t.Fatalf("expected allocation info")
	}
	if allocationInfo.OwnerPoolName == "" {
		t.Fatalf("expected owner pool name, got empty")
	}
	if allocationInfo.AllocationResult.IsEmpty() {
		t.Fatalf("expected non-empty allocation result")
	}
}
