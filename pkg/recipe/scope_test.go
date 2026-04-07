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
	"testing"

	"github.com/NVIDIA/aicr/pkg/measurement"
)

func TestConstraint_EffectiveScope(t *testing.T) {
	tests := []struct {
		name string
		c    Constraint
		want measurement.Scope
	}{
		{
			name: "K8s constraint inferred cluster",
			c:    Constraint{Name: "K8s.server.version", Value: ">= 1.32"},
			want: measurement.ScopeCluster,
		},
		{
			name: "OS constraint inferred node",
			c:    Constraint{Name: "OS.release.ID", Value: "ubuntu"},
			want: measurement.ScopeNode,
		},
		{
			name: "GPU constraint inferred node",
			c:    Constraint{Name: "GPU.driver.version", Value: ">= 570"},
			want: measurement.ScopeNode,
		},
		{
			name: "SystemD constraint inferred node",
			c:    Constraint{Name: "SystemD.service.nvidia-persistenced", Value: "active"},
			want: measurement.ScopeNode,
		},
		{
			name: "NodeTopology constraint inferred node",
			c:    Constraint{Name: "NodeTopology.labels.gpu-class", Value: "h100"},
			want: measurement.ScopeNode,
		},
		{
			name: "explicit node override beats cluster default",
			c:    Constraint{Name: "K8s.nodes.ready", Value: "true", Scope: "node"},
			want: measurement.ScopeNode,
		},
		{
			name: "explicit cluster override beats node default",
			c:    Constraint{Name: "OS.release.ID", Value: "ubuntu", Scope: "cluster"},
			want: measurement.ScopeCluster,
		},
		{
			name: "invalid explicit scope falls back to type default",
			c:    Constraint{Name: "OS.release.ID", Value: "ubuntu", Scope: "global"},
			want: measurement.ScopeNode,
		},
		{
			name: "unknown measurement type falls back to cluster",
			c:    Constraint{Name: "Bogus.subtype.key", Value: "x"},
			want: measurement.ScopeCluster,
		},
		{
			name: "malformed name falls back to cluster",
			c:    Constraint{Name: "nodots", Value: "x"},
			want: measurement.ScopeCluster,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.EffectiveScope(); got != tt.want {
				t.Errorf("EffectiveScope() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterConstraintsByScope(t *testing.T) {
	cs := []Constraint{
		{Name: "K8s.server.version", Value: ">= 1.32"},
		{Name: "OS.release.ID", Value: "ubuntu"},
		{Name: "OS.sysctl./proc/sys/kernel/osrelease", Value: ">= 6.8"},
		{Name: "GPU.driver.version", Value: ">= 570"},
		{Name: "K8s.nodes.ready", Value: "true", Scope: "node"},
	}

	node := FilterConstraintsByScope(cs, measurement.ScopeNode)
	if len(node) != 4 {
		t.Errorf("expected 4 node-scoped constraints, got %d", len(node))
	}
	for _, c := range node {
		if c.Name == "K8s.server.version" {
			t.Errorf("K8s.server.version should not be in node scope")
		}
	}

	cluster := FilterConstraintsByScope(cs, measurement.ScopeCluster)
	if len(cluster) != 1 {
		t.Errorf("expected 1 cluster-scoped constraint, got %d", len(cluster))
	}
	if cluster[0].Name != "K8s.server.version" {
		t.Errorf("expected K8s.server.version in cluster scope, got %s", cluster[0].Name)
	}

	empty := FilterConstraintsByScope(nil, measurement.ScopeNode)
	if len(empty) != 0 {
		t.Errorf("expected empty slice from nil input, got %v", empty)
	}
}
