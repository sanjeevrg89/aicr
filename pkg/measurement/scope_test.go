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

import "testing"

func TestType_DefaultScope(t *testing.T) {
	tests := []struct {
		name string
		typ  Type
		want Scope
	}{
		{"K8s is cluster", TypeK8s, ScopeCluster},
		{"GPU is node", TypeGPU, ScopeNode},
		{"OS is node", TypeOS, ScopeNode},
		{"SystemD is node", TypeSystemD, ScopeNode},
		{"NodeTopology is node", TypeNodeTopology, ScopeNode},
		{"unknown type defaults cluster", Type("Unknown"), ScopeCluster},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.typ.DefaultScope(); got != tt.want {
				t.Errorf("%s.DefaultScope() = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestIsValidScope(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		want  bool
	}{
		{"cluster valid", ScopeCluster, true},
		{"node valid", ScopeNode, true},
		{"empty invalid", Scope(""), false},
		{"bogus invalid", Scope("global"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidScope(tt.scope); got != tt.want {
				t.Errorf("IsValidScope(%q) = %v, want %v", tt.scope, got, tt.want)
			}
		})
	}
}
