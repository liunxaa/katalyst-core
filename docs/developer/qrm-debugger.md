# QRM Debugger

`qrm-debugger` runs the CPU QRM dynamic policy in-process with a dummy CPU
topology and a JSON pod scenario. It is intended for inspecting how
`GetTopologyHints` and `Allocate` change hints, `AllocationInfo`, `PodEntries`,
and `MachineState` without starting kubelet or katalyst-agent.

Start the interactive UI:

```bash
go run ./cmd/qrm-debugger -web 127.0.0.1:18080
```

The UI lets you add requests for `shared_cores`, `dedicated_cores`,
`reclaimed_cores`, and `system_cores`, run the scenario, then inspect hints,
allocation output, cache snapshots, and cache diffs for each step.
The topology panel also exposes the dynamic knobs that affect reclaim pool
calculation, including `enableReclaim`, `allowSharedCoresOverlapReclaimedCores`,
and `reservedReclaimedCPUsSize`.

Run the sample scenario:

```bash
go run ./cmd/qrm-debugger \
  -scenario examples/qrm-debugger/cpu-dynamic-scenario.json \
  -format markdown \
  -out /tmp/qrm-trace.md
```

Generate JSON instead:

```bash
go run ./cmd/qrm-debugger \
  -scenario examples/qrm-debugger/cpu-dynamic-scenario.json \
  -format json
```

Create a new scenario:

```bash
go run ./cmd/qrm-debugger -print-sample > /tmp/qrm-scenario.json
```

To add a pod, append an item under `pods`. The tool converts each container into
a CPU `ResourceRequest`, calls `GetTopologyHints`, chooses a hint according to
`hintSelection`, then calls `Allocate` unless `skipAllocate` is true.

Important fields:

- `topology.cpus`, `topology.sockets`, `topology.numas`: dummy CPU topology.
- `reservedCPUs`: number of CPUs reserved for system use. If omitted, the tool
  reserves 2 CPUs, matching the dynamic policy unit-test harness. Set it to `0`
  to disable reservation in a scenario.
- `reservedReclaimedCPUsSize`: fallback/minimum size used by the CPU dynamic
  policy when generating the reclaim pool. If omitted, the policy uses
  `max(4, numa count)`.
- `enableReclaim`: mirrors
  `dynamicConfig.GetDynamicConfiguration().EnableReclaim` and changes how the
  reclaim pool is apportioned in allocation.
- `allowSharedCoresOverlapReclaimedCores`: initializes the CPU plugin state flag
  with the same name. When enabled, the trace snapshots include the current
  `reclaim_overlap_share_ratio` computed from `PodEntries`.
- `hintSelection`: `first-preferred`, `first`, or `none`.
- `qos`: one of `shared_cores`, `dedicated_cores`, `reclaimed_cores`, or
  `system_cores`.
- `numaBinding`: when true, adds both the flattened `numa_binding=true`
  annotation and the `katalyst.kubewharf.io/memory_enhancement` JSON annotation
  that the top-level QRM request parser preserves.
- `containers[].hintNodes`: explicit hint nodes for `Allocate`. If omitted, the
  tool picks a hint from the `GetTopologyHints` response.
- `containers[].skipAllocate`: only run `GetTopologyHints` for that container.

The Markdown output includes a Mermaid flowchart plus per-step JSON blocks for
the selected hint, hint response, allocation response, and the state after
allocation.
