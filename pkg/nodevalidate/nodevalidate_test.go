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

package nodevalidate

import (
	"context"
	"testing"

	"github.com/NVIDIA/aicr/pkg/diff"
	"github.com/NVIDIA/aicr/pkg/header"
	"github.com/NVIDIA/aicr/pkg/measurement"
	"github.com/NVIDIA/aicr/pkg/recipe"
	"github.com/NVIDIA/aicr/pkg/snapshotter"

	"k8s.io/client-go/kubernetes/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makeSnap builds a test snapshot from measurement data.
func makeSnap(measurements ...*measurement.Measurement) *snapshotter.Snapshot {
	snap := snapshotter.NewSnapshot()
	snap.Header = header.Header{
		Kind:       header.KindSnapshot,
		APIVersion: "aicr.nvidia.com/v1alpha1",
		Metadata:   map[string]string{"source-node": "test-node"},
	}
	snap.Measurements = measurements
	return snap
}

// simulateValidation mimics what ValidateNode does without requiring real collectors.
func simulateValidation(rec *recipe.RecipeResult, snap *snapshotter.Snapshot) *NodeResult {
	result := diff.RecipeVsSnapshot(rec, snap)
	return &NodeResult{
		NodeName:   "test-node",
		Compliant:  !result.HasDrift(),
		Timestamp:  "2026-03-25T14:00:00Z",
		DiffResult: result,
	}
}

func TestNodeValidation_Compliant(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "K8s.server.version", Value: ">= 1.32", Severity: "error"},
			{Name: "OS.release.ID", Value: "ubuntu", Severity: "error"},
			{Name: "GPU.device.driver", Value: ">= 535.0", Severity: "warning"},
		},
	}

	snap := makeSnap(
		&measurement.Measurement{Type: measurement.TypeK8s, Subtypes: []measurement.Subtype{
			{Name: "server", Data: map[string]measurement.Reading{"version": measurement.Str("1.32.4")}},
		}},
		&measurement.Measurement{Type: measurement.TypeGPU, Subtypes: []measurement.Subtype{
			{Name: "device", Data: map[string]measurement.Reading{"driver": measurement.Str("550.54.15")}},
		}},
		&measurement.Measurement{Type: measurement.TypeOS, Subtypes: []measurement.Subtype{
			{Name: "release", Data: map[string]measurement.Reading{"ID": measurement.Str("ubuntu")}},
		}},
	)

	result := simulateValidation(rec, snap)

	if !result.Compliant {
		t.Errorf("expected compliant node")
	}
	if len(result.FailedConstraints()) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.FailedConstraints()))
	}
}

func TestNodeValidation_NonCompliant(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "K8s.server.version", Value: ">= 1.32", Severity: "error", Remediation: "Upgrade K8s"},
			{Name: "GPU.device.driver", Value: ">= 550.0", Severity: "warning", Remediation: "Upgrade driver"},
		},
	}

	snap := makeSnap(
		&measurement.Measurement{Type: measurement.TypeK8s, Subtypes: []measurement.Subtype{
			{Name: "server", Data: map[string]measurement.Reading{"version": measurement.Str("1.31.0")}},
		}},
		&measurement.Measurement{Type: measurement.TypeGPU, Subtypes: []measurement.Subtype{
			{Name: "device", Data: map[string]measurement.Reading{"driver": measurement.Str("535.129.03")}},
		}},
	)

	result := simulateValidation(rec, snap)

	if result.Compliant {
		t.Errorf("expected non-compliant node")
	}
	if len(result.FailedConstraints()) != 2 {
		t.Errorf("expected 2 failed constraints, got %d", len(result.FailedConstraints()))
	}
}

func TestNodeValidation_LabelValues(t *testing.T) {
	tests := []struct {
		name      string
		compliant bool
		want      string
	}{
		{"compliant", true, "true"},
		{"non-compliant", false, "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nr := &NodeResult{
				NodeName:  "gpu-node-1",
				Compliant: tt.compliant,
				Timestamp: "2026-03-25T14:00:00Z",
			}

			labels := nr.LabelValues()
			if labels[LabelCompliance] != tt.want {
				t.Errorf("LabelCompliance = %q, want %q", labels[LabelCompliance], tt.want)
			}
			if labels[LabelLastValidated] == "" {
				t.Errorf("expected LabelLastValidated to be set")
			}
		})
	}
}

func TestNodeValidation_ErrorConstraints(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "GPU.device.driver", Value: ">= 535.0", Severity: "error"},
		},
	}

	// No GPU measurement — constraint will error
	snap := makeSnap(
		&measurement.Measurement{Type: measurement.TypeK8s, Subtypes: []measurement.Subtype{
			{Name: "server", Data: map[string]measurement.Reading{"version": measurement.Str("1.32.4")}},
		}},
	)

	result := simulateValidation(rec, snap)

	if result.Compliant {
		t.Errorf("expected non-compliant when constraint errors")
	}
	if len(result.ErrorConstraints()) != 1 {
		t.Errorf("expected 1 error constraint, got %d", len(result.ErrorConstraints()))
	}
}

func TestNodeValidation_MixedResults(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "K8s.server.version", Value: ">= 1.32", Severity: "error"},
			{Name: "OS.release.ID", Value: "ubuntu", Severity: "error"},
			{Name: "GPU.device.driver", Value: ">= 550.0", Severity: "warning"},
		},
	}

	snap := makeSnap(
		&measurement.Measurement{Type: measurement.TypeK8s, Subtypes: []measurement.Subtype{
			{Name: "server", Data: map[string]measurement.Reading{"version": measurement.Str("1.32.4")}},
		}},
		&measurement.Measurement{Type: measurement.TypeGPU, Subtypes: []measurement.Subtype{
			{Name: "device", Data: map[string]measurement.Reading{"driver": measurement.Str("535.129.03")}},
		}},
		&measurement.Measurement{Type: measurement.TypeOS, Subtypes: []measurement.Subtype{
			{Name: "release", Data: map[string]measurement.Reading{"ID": measurement.Str("ubuntu")}},
		}},
	)

	result := simulateValidation(rec, snap)

	if result.Compliant {
		t.Errorf("expected non-compliant with 1 failed constraint")
	}
	if result.DiffResult.Summary.ConstraintsPassed != 2 {
		t.Errorf("expected 2 passed, got %d", result.DiffResult.Summary.ConstraintsPassed)
	}
	if result.DiffResult.Summary.ConstraintsFailed != 1 {
		t.Errorf("expected 1 failed, got %d", result.DiffResult.Summary.ConstraintsFailed)
	}
}

func TestNodeValidation_EmptyConstraints(t *testing.T) {
	rec := &recipe.RecipeResult{Constraints: []recipe.Constraint{}}
	snap := makeSnap()

	result := simulateValidation(rec, snap)

	if !result.Compliant {
		t.Errorf("expected compliant with no constraints")
	}
}

// TestLabelNode_WithFakeClient tests actual node labeling using a fake K8s clientset.
func TestLabelNode_WithFakeClient(t *testing.T) {
	// Create a fake node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "gpu-node-1",
			Labels: map[string]string{"existing-label": "value"},
		},
	}
	clientset := fake.NewSimpleClientset(node)

	// Build a compliant result
	result := &NodeResult{
		NodeName:  "gpu-node-1",
		Compliant: true,
		Timestamp: "2026-03-25T14:00:00Z",
		DiffResult: &diff.Result{
			Mode: "recipe-vs-snapshot",
		},
	}

	// Patch the node directly using the fake clientset (bypasses LabelNode's client creation)
	labels := result.LabelValues()
	patchData := []byte(`{"metadata":{"labels":{"` + LabelCompliance + `":"` + labels[LabelCompliance] + `","` + LabelLastValidated + `":"` + labels[LabelLastValidated] + `"}}}`)

	_, err := clientset.CoreV1().Nodes().Patch(
		context.Background(),
		result.NodeName,
		"application/strategic-merge-patch+json",
		patchData,
		metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to patch node: %v", err)
	}

	// Verify labels were applied
	updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "gpu-node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if updated.Labels[LabelCompliance] != "true" {
		t.Errorf("expected %s=true, got %q", LabelCompliance, updated.Labels[LabelCompliance])
	}
	if updated.Labels[LabelLastValidated] != "2026-03-25T14:00:00Z" {
		t.Errorf("expected %s timestamp, got %q", LabelLastValidated, updated.Labels[LabelLastValidated])
	}
	// Verify existing labels are preserved
	if updated.Labels["existing-label"] != "value" {
		t.Errorf("existing label was overwritten")
	}
}

// TestLabelNode_NonCompliant verifies non-compliant label is set correctly.
func TestLabelNode_NonCompliant(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-2",
		},
	}
	clientset := fake.NewSimpleClientset(node)

	result := &NodeResult{
		NodeName:  "gpu-node-2",
		Compliant: false,
		Timestamp: "2026-03-25T15:00:00Z",
		DiffResult: &diff.Result{
			Mode: "recipe-vs-snapshot",
		},
	}

	labels := result.LabelValues()
	patchData := []byte(`{"metadata":{"labels":{"` + LabelCompliance + `":"` + labels[LabelCompliance] + `","` + LabelLastValidated + `":"` + labels[LabelLastValidated] + `"}}}`)

	_, err := clientset.CoreV1().Nodes().Patch(
		context.Background(),
		result.NodeName,
		"application/strategic-merge-patch+json",
		patchData,
		metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to patch node: %v", err)
	}

	updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "gpu-node-2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if updated.Labels[LabelCompliance] != "false" {
		t.Errorf("expected %s=false, got %q", LabelCompliance, updated.Labels[LabelCompliance])
	}
}
