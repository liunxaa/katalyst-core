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

package policy

import (
	"fmt"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type HintOptimizerFactoryOptions struct {
	Conf         *config.Configuration
	MetaServer   *metaserver.MetaServer
	Emitter      metrics.MetricEmitter
	State        state.State
	ReservedCPUs machine.CPUSet
	MachineInfo  *machine.KatalystMachineInfo

	// GetContainerRequestedCores returns the requested cpu cores for a single
	// allocation. Filters that need to aggregate per-pod requests (e.g. SNB
	// threshold) consume this callback to stay decoupled from DynamicPolicy.
	GetContainerRequestedCores state.GetContainerRequestedCoresFunc
	// SNBCPUTotalRequestThresholdRatio is the per-NUMA SNB+DNB cpu request
	// threshold ratio. A value <= 0 disables the SNB threshold filter.
	SNBCPUTotalRequestThresholdRatio float64
}

type HintOptimizerFactory func(options HintOptimizerFactoryOptions) (hintoptimizer.HintOptimizer, error)

type HintOptimizerRegistry map[string]HintOptimizerFactory

func (h *HintOptimizerRegistry) Register(name string, factory HintOptimizerFactory) {
	(*h)[name] = factory
}

func (h *HintOptimizerRegistry) HintOptimizer(policies []string, options HintOptimizerFactoryOptions) (hintoptimizer.HintOptimizer, error) {
	optimizers := make([]namedHintOptimizer, 0, len(policies))
	for _, name := range policies {
		f, ok := (*h)[name]
		if !ok {
			return nil, fmt.Errorf("hint namedHintOptimizer %s not registered", name)
		}

		o, err := f(options)
		if err != nil {
			return nil, fmt.Errorf("hint namedHintOptimizer %s failed with error: %v", name, err)
		}

		optimizers = append(optimizers, namedHintOptimizer{
			name:          name,
			hintOptimizer: o,
		})
	}
	return &multiHintOptimizer{
		optimizers: optimizers,
	}, nil
}

type namedHintOptimizer struct {
	name          string
	hintOptimizer hintoptimizer.HintOptimizer
}

type multiHintOptimizer struct {
	optimizers []namedHintOptimizer
}

func (m multiHintOptimizer) OptimizeHints(request hintoptimizer.Request, hints *pluginapi.ListOfTopologyHints) error {
	for _, optimizer := range m.optimizers {
		err := optimizer.hintOptimizer.OptimizeHints(request, hints)
		if err != nil {
			if hintoptimizerutil.IsSkipOptimizeHintsError(err) {
				general.Warningf("hint optimizer %s continue with error: %v", optimizer.name, err.Error())
				continue
			}
			return err
		}
		// if no error, return directly
		// todo: support post-filtering hints later
		return nil
	}
	return nil
}

func (m multiHintOptimizer) Run(stopCh <-chan struct{}) error {
	var errList []error
	for _, optimizer := range m.optimizers {
		err := optimizer.hintOptimizer.Run(stopCh)
		if err != nil {
			errList = append(errList, err)
		}
	}
	return utilerrors.NewAggregate(errList)
}

type HintFilterFactory func(options HintOptimizerFactoryOptions) (hintoptimizer.HintFilter, error)

type HintFilterRegistry map[string]HintFilterFactory

func (h *HintFilterRegistry) Register(name string, factory HintFilterFactory) {
	(*h)[name] = factory
}

// HintFilter constructs a chained HintFilter from the given filter names. Filters
// are executed in the same order as the slice; an error from any single filter
// terminates the whole chain.
func (h *HintFilterRegistry) HintFilter(names []string, options HintOptimizerFactoryOptions) (hintoptimizer.HintFilter, error) {
	filters := make([]namedHintFilter, 0, len(names))
	for _, name := range names {
		f, ok := (*h)[name]
		if !ok {
			return nil, fmt.Errorf("hint filter %s not registered", name)
		}

		filter, err := f(options)
		if err != nil {
			return nil, fmt.Errorf("hint filter %s failed with error: %v", name, err)
		}

		filters = append(filters, namedHintFilter{
			name:       name,
			hintFilter: filter,
		})
	}
	return &multiHintFilter{filters: filters}, nil
}

type namedHintFilter struct {
	name       string
	hintFilter hintoptimizer.HintFilter
}

// multiHintFilter executes a list of HintFilters in registration order. Unlike
// multiHintOptimizer, all filters are executed (no short-circuit on success);
// the chain is only aborted when a filter returns a non-nil error.
type multiHintFilter struct {
	filters []namedHintFilter
}

func (m multiHintFilter) Filter(request hintoptimizer.Request, hints *pluginapi.ListOfTopologyHints) error {
	for _, f := range m.filters {
		if err := f.hintFilter.Filter(request, hints); err != nil {
			general.Warningf("hint filter %s rejected hints with error: %v", f.name, err.Error())
			return err
		}
	}
	return nil
}

func (m multiHintFilter) Run(stopCh <-chan struct{}) error {
	var errList []error
	for _, f := range m.filters {
		if err := f.hintFilter.Run(stopCh); err != nil {
			errList = append(errList, err)
		}
	}
	return utilerrors.NewAggregate(errList)
}
