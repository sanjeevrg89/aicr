# ADR 005: Node-Scoped Constraints

**Status:** Proposed
**Date:** 2026-04-07
**Related:** `aicr node-validate` (Phase 2), fleet observability RFC (#464)

## Context

`aicr node-validate` is designed to run as a DaemonSet on every node, collect a
local snapshot, evaluate recipe constraints against that snapshot, and label the
node with compliance status. It reuses `constraints.Evaluate()` and
`diff.RecipeVsSnapshot()` so that the same constraint semantics power `aicr
diff`, `aicr validate`, and per-node compliance.

That reuse is the problem. Recipes today mix two kinds of constraints in a
single `constraints:` list:

- **Cluster-scoped**: answerable only by looking at cluster-wide state. Example
  from `recipes/overlays/h100-eks-training.yaml`:
  ```yaml
  constraints:
    - name: K8s.server.version
      value: ">= 1.32.4"
  ```
- **Node-scoped**: answerable from a single node's local state. Example from
  `recipes/overlays/h100-eks-ubuntu-training.yaml`:
  ```yaml
  constraints:
    - name: OS.release.ID
      value: ubuntu
    - name: OS.sysctl./proc/sys/kernel/osrelease
      value: ">= 6.8"
  ```

A DaemonSet running `constraints.Evaluate()` against a single-node snapshot
cannot correctly evaluate `K8s.server.version`. The node's local snapshot does
not contain that measurement, so the constraint either errors out or the
extractor returns a misleading "not found" result. Either way, the node gets
labeled incorrectly.

This was flagged in the #464 review:

> The RFC says Phase 2 can reuse `constraints.Evaluate()` for per-node
> compliance, but the current constraint model is not node-addressable. ...
> A DaemonSet cannot correctly label a node as "recipe-compliant" by evaluating
> the current full constraint set.

## Goals

1. Make constraint scope explicit and machine-checkable.
2. Zero migration cost for existing recipes. Current overlays must keep working
   without edits.
3. `aicr node-validate` must only evaluate constraints a node can actually
   answer.
4. `aicr validate` and `aicr diff` behavior against a cluster-wide snapshot
   must not change.
5. Leave room for ambiguous types (e.g., `NodeTopology`) without forcing a
   schema change every time.

## Non-Goals

- Changing the constraint expression syntax or evaluation semantics.
- Changing measurement types or subtypes.
- Per-node labels on the metrics path (tracked separately in the Phase 3
  rework).

## Decision

Introduce a two-layer scope model: **type-level default** plus **per-constraint
override**.

### Layer 1: Type-level default scope

Each `measurement.Type` gets a default scope classification:

| Type            | Default scope | Rationale                                             |
|-----------------|---------------|-------------------------------------------------------|
| `K8s`           | `cluster`     | Server version, node lists, pods are cluster state   |
| `GPU`           | `node`        | Driver, model, count, memory are node-local          |
| `OS`            | `node`        | Release, sysctl, kernel are node-local               |
| `SystemD`       | `node`        | Service state is node-local                          |
| `NodeTopology`  | `node`        | Per-node taint/label facts                           |

Defined in a new `pkg/measurement/scope.go`:

```go
type Scope string

const (
    ScopeCluster Scope = "cluster"
    ScopeNode    Scope = "node"
)

var defaultScopeByType = map[Type]Scope{
    TypeK8s:          ScopeCluster,
    TypeGPU:          ScopeNode,
    TypeOS:           ScopeNode,
    TypeSystemD:      ScopeNode,
    TypeNodeTopology: ScopeNode,
}

func (t Type) DefaultScope() Scope {
    if s, ok := defaultScopeByType[t]; ok {
        return s
    }
    return ScopeCluster // conservative default for unknown types
}
```

### Layer 2: Per-constraint override

Recipe authors can override the default by adding a `scope` field to any
constraint. This is additive and backwards compatible: existing constraints
without `scope` inherit the type default.

```yaml
constraints:
  - name: K8s.server.version     # inherits cluster from type default
    value: ">= 1.32.4"
  - name: OS.release.ID          # inherits node from type default
    value: ubuntu
  - name: NodeTopology.labels.gpu-class   # explicit override
    value: h100
    scope: node
```

The `Constraint` struct in `pkg/recipe` gains an optional `Scope` field:

```go
type Constraint struct {
    Name  string `yaml:"name"`
    Value string `yaml:"value"`
    Scope string `yaml:"scope,omitempty"` // "" | "node" | "cluster"
}

func (c *Constraint) EffectiveScope() measurement.Scope {
    if c.Scope != "" {
        return measurement.Scope(c.Scope)
    }
    path, err := constraints.ParseConstraintPath(c.Name)
    if err != nil {
        return measurement.ScopeCluster
    }
    return path.Type.DefaultScope()
}
```

### Layer 3: Filtering at evaluation time

A new function in `pkg/diff` filters constraints by scope before evaluation:

```go
// FilterByScope returns constraints matching the given scope. Used by
// node-validate to select only node-answerable constraints.
func FilterByScope(cs []recipe.Constraint, scope measurement.Scope) []recipe.Constraint {
    out := make([]recipe.Constraint, 0, len(cs))
    for _, c := range cs {
        if c.EffectiveScope() == scope {
            out = append(out, c)
        }
    }
    return out
}
```

`pkg/nodevalidate.ValidateNode` calls this before invoking
`diff.RecipeVsSnapshot`:

```go
// Only evaluate constraints the node can answer
nodeRecipe := *rec
nodeRecipe.Constraints = diff.FilterByScope(rec.Constraints, measurement.ScopeNode)
// phase constraints filtered the same way
result := diff.RecipeVsSnapshot(&nodeRecipe, snap)
```

`aicr diff` and `aicr validate` do not filter. They evaluate the full
constraint set, same as today.

## Label contract

The `aicr.nvidia.com/recipe-compliant` label is set by `node-validate` based
on the result of evaluating **only node-scoped constraints**. This has
implications that must be documented before the label becomes external API
surface:

1. **What "compliant" means**: all node-scoped constraints in the recipe
   passed on this node's local snapshot at the last validation run. It says
   nothing about cluster-scoped constraints. A cluster-wide compliance view
   requires `aicr diff` or `aicr validate`.

2. **Transition semantics**: the label is set on every validation run.
   Compliant-to-non-compliant transitions happen on the next run after a
   constraint starts failing. There is no debouncing. If consumers need
   stability, they should observe the label over multiple intervals.

3. **Recipe changes**: if the recipe changes (new constraints, different
   values), the next validation run reflects the new recipe. There is no
   carryover. Workloads selecting on the label may become unschedulable if the
   new recipe is stricter. This is intentional: the label reflects the current
   recipe, not a historical one.

4. **Workloads running during a flip**: the label only controls future
   scheduling decisions via `nodeSelector`. Kubernetes does not evict running
   pods when a label they selected on changes. Consumers who need eviction
   must add their own controller or use taints (not in scope for this ADR).

5. **Missing label**: a node without the label has not yet been validated.
   Consumers should treat missing as "unknown", not as "compliant" or
   "non-compliant".

6. **`aicr.nvidia.com/last-validated`**: timestamp of the last validation run,
   RFC3339. Consumers can use this to detect stale validations.

## Migration

None required. Every existing recipe works without edits because every
existing constraint's type already maps to a sensible default scope:

- `K8s.*` constraints stay cluster-scoped, same as today's implicit behavior.
- `GPU.*`, `OS.*`, `SystemD.*` constraints become node-scoped, which is what
  they already are in practice.

The only observable change is that `aicr node-validate` starts filtering
constraints before evaluation. Without this ADR, `node-validate` evaluates
cluster-scoped constraints against node-local snapshots and produces wrong
labels.

## Alternatives considered

### A. Collector-level filtering

Filter constraints in `nodevalidate.collectLocalSnapshot` by only running
collectors whose output is node-local. The extractor would then fail to find
cluster-scoped constraint data and the evaluation would error.

Rejected: implicit, not declarative. A recipe author cannot see which of their
constraints will be evaluated on a node without tracing through collector
code. Also, "evaluation errored" is indistinguishable from "constraint
violated" in the current diff summary, which is the exact bug we are trying to
fix.

### B. Separate `nodeConstraints:` section in recipe

Split `constraints:` into `constraints:` (cluster) and `nodeConstraints:`
(per-node).

Rejected: migration cost. Every overlay with node-scoped constraints
(`h100-eks-ubuntu-training.yaml`, `gb200-eks-ubuntu-training.yaml`, etc.)
would need edits. Also loses the invariant that "one constraint list is the
complete contract for this recipe."

### C. Per-subtype scope classification

Go finer than type and classify by `Type.Subtype` pair. For example,
`K8s.server.version` is cluster, `K8s.nodes.*` could be node.

Rejected: premature. No current constraint needs subtype-level scope.
`NodeTopology` is the only type where ambiguity is plausible, and the
per-constraint `scope:` override handles that case without adding a second
lookup table.

## Rollout

1. Land this ADR.
2. Implement `measurement.Scope`, `Type.DefaultScope`,
   `Constraint.EffectiveScope`, and `diff.FilterByScope`.
3. Update `pkg/nodevalidate.ValidateNode` to filter before evaluation.
4. Add unit tests for filtering, including mixed-scope recipes.
5. Document the label contract in `docs/integrator/` (consumer facing).
6. No changes to existing recipe YAML. No changes to `aicr diff` or
   `aicr validate`.

## Open questions

- Do we want `aicr diff --scope=node` as a convenience for running the same
  filtering locally without a DaemonSet? Probably yes, but additive and out of
  scope here.
- Should the `aicr_node_compliant` gauge carry a label for
  "cluster-constraints-unknown" so consumers can tell this is a node-scoped
  view, not a full compliance view? Tracked in the Phase 3 metrics rework.
