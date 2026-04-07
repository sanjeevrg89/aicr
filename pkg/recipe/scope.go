// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package recipe

import (
	"strings"

	"github.com/NVIDIA/aicr/pkg/measurement"
)

// EffectiveScope returns the scope of a constraint, applying the explicit
// override if present or inferring from the constraint's measurement type.
//
// Resolution order:
//  1. Explicit Scope field on the constraint (if valid)
//  2. Default scope of the measurement type parsed from the constraint Name
//  3. ScopeCluster as a conservative fallback
//
// See docs/design/005-node-scoped-constraints.md.
func (c *Constraint) EffectiveScope() measurement.Scope {
	if c.Scope != "" {
		s := measurement.Scope(c.Scope)
		if measurement.IsValidScope(s) {
			return s
		}
	}

	// Infer from measurement type. Constraint names use the fully qualified
	// path format "{Type}.{Subtype}.{Key}". We only need the Type prefix here
	// so we avoid importing pkg/constraints (which would cause a cycle).
	idx := strings.IndexByte(c.Name, '.')
	if idx <= 0 {
		return measurement.ScopeCluster
	}
	typeStr := c.Name[:idx]
	mt, ok := measurement.ParseType(typeStr)
	if !ok {
		return measurement.ScopeCluster
	}
	return mt.DefaultScope()
}

// FilterConstraintsByScope returns the subset of constraints whose effective
// scope matches the requested scope. Used by node-validate to select only
// node-answerable constraints.
func FilterConstraintsByScope(cs []Constraint, scope measurement.Scope) []Constraint {
	out := make([]Constraint, 0, len(cs))
	for i := range cs {
		if cs[i].EffectiveScope() == scope {
			out = append(out, cs[i])
		}
	}
	return out
}

// ScopedView returns a shallow copy of the RecipeResult with top-level and
// phase-level constraints filtered to the requested scope. Components are
// preserved unchanged. Used by aicr node-validate to evaluate only
// constraints the node can answer locally.
//
// The original RecipeResult is not mutated.
func (r *RecipeResult) ScopedView(scope measurement.Scope) *RecipeResult {
	if r == nil {
		return nil
	}
	view := *r
	view.Constraints = FilterConstraintsByScope(r.Constraints, scope)

	if r.Validation != nil {
		filtered := *r.Validation
		filtered.Readiness = scopedPhase(r.Validation.Readiness, scope)
		filtered.Deployment = scopedPhase(r.Validation.Deployment, scope)
		filtered.Performance = scopedPhase(r.Validation.Performance, scope)
		filtered.Conformance = scopedPhase(r.Validation.Conformance, scope)
		view.Validation = &filtered
	}
	return &view
}

// scopedPhase returns a copy of the phase with constraints filtered to the
// requested scope. Checks, node selection, and other fields are preserved.
func scopedPhase(p *ValidationPhase, scope measurement.Scope) *ValidationPhase {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Constraints = FilterConstraintsByScope(p.Constraints, scope)
	return &cp
}
