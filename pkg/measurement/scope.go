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

package measurement

// Scope describes whether a measurement (and therefore a constraint over it)
// is answerable from a single node's local state or requires cluster-wide
// state.
//
// See docs/design/005-node-scoped-constraints.md for the full design.
type Scope string

const (
	// ScopeCluster means the measurement describes cluster-wide state and
	// cannot be evaluated from a single node's local snapshot.
	// Example: K8s.server.version.
	ScopeCluster Scope = "cluster"

	// ScopeNode means the measurement describes per-node state and can be
	// evaluated from a single node's local snapshot.
	// Example: OS.release.ID, GPU.driver.
	ScopeNode Scope = "node"
)

// defaultScopeByType maps each measurement Type to its default scope. Recipe
// constraints inherit this unless overridden by an explicit scope field on the
// constraint.
var defaultScopeByType = map[Type]Scope{
	TypeK8s:          ScopeCluster,
	TypeGPU:          ScopeNode,
	TypeOS:           ScopeNode,
	TypeSystemD:      ScopeNode,
	TypeNodeTopology: ScopeNode,
}

// DefaultScope returns the default scope for a measurement type. Unknown
// types default to ScopeCluster as the conservative choice.
func (t Type) DefaultScope() Scope {
	if s, ok := defaultScopeByType[t]; ok {
		return s
	}
	return ScopeCluster
}

// IsValidScope returns true if s is a recognized scope value.
func IsValidScope(s Scope) bool {
	return s == ScopeCluster || s == ScopeNode
}
