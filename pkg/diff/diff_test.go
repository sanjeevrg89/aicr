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

package diff

import (
	"testing"

	"github.com/NVIDIA/aicr/pkg/header"
	"github.com/NVIDIA/aicr/pkg/measurement"
	"github.com/NVIDIA/aicr/pkg/recipe"
	"github.com/NVIDIA/aicr/pkg/snapshotter"
)

func makeSnapshot(measurements ...*measurement.Measurement) *snapshotter.Snapshot {
	snap := snapshotter.NewSnapshot()
	snap.Header = header.Header{
		Kind:       header.KindSnapshot,
		APIVersion: "aicr.nvidia.com/v1alpha1",
		Metadata:   map[string]string{},
	}
	snap.Measurements = measurements
	return snap
}

func makeMeasurement(t measurement.Type, subtypes ...measurement.Subtype) *measurement.Measurement {
	return &measurement.Measurement{
		Type:     t,
		Subtypes: subtypes,
	}
}

func makeSubtype(name string, data map[string]measurement.Reading) measurement.Subtype {
	return measurement.Subtype{
		Name: name,
		Data: data,
	}
}

// --- Snapshot-vs-Snapshot Tests ---

func TestSnapshots_IdenticalSnapshots(t *testing.T) {
	snap := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version":  measurement.Str("1.32.4"),
				"platform": measurement.Str("eks"),
			}),
		),
	)

	result := Snapshots(snap, snap)

	if result.HasDrift() {
		t.Errorf("expected no drift for identical snapshots, got %d changes", result.Summary.Total)
	}
	if result.Mode != "snapshot-vs-snapshot" {
		t.Errorf("expected mode snapshot-vs-snapshot, got %s", result.Mode)
	}
}

func TestSnapshots_ModifiedReading(t *testing.T) {
	baseline := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.31.0"),
			}),
		),
	)
	target := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.32.4"),
			}),
		),
	)

	result := Snapshots(baseline, target)

	if result.Summary.Modified != 1 {
		t.Fatalf("expected 1 modified, got %d", result.Summary.Modified)
	}

	c := result.Changes[0]
	if c.Kind != Modified {
		t.Errorf("expected Modified, got %s", c.Kind)
	}
	if c.Severity != SeverityInfo {
		t.Errorf("expected severity info, got %s", c.Severity)
	}
	if c.Path != "K8s.server.version" {
		t.Errorf("expected path K8s.server.version, got %s", c.Path)
	}
	if c.Baseline != "1.31.0" || c.Target != "1.32.4" {
		t.Errorf("expected 1.31.0 → 1.32.4, got %s → %s", c.Baseline, c.Target)
	}
}

func TestSnapshots_AddedReading(t *testing.T) {
	baseline := makeSnapshot(
		makeMeasurement(measurement.TypeGPU,
			makeSubtype("device", map[string]measurement.Reading{
				"driver": measurement.Str("535.129.03"),
			}),
		),
	)
	target := makeSnapshot(
		makeMeasurement(measurement.TypeGPU,
			makeSubtype("device", map[string]measurement.Reading{
				"driver": measurement.Str("535.129.03"),
				"model":  measurement.Str("H100"),
			}),
		),
	)

	result := Snapshots(baseline, target)
	if result.Summary.Added != 1 {
		t.Fatalf("expected 1 added, got %d", result.Summary.Added)
	}
}

func TestSnapshots_RemovedReading(t *testing.T) {
	baseline := makeSnapshot(
		makeMeasurement(measurement.TypeOS,
			makeSubtype("release", map[string]measurement.Reading{
				"ID":      measurement.Str("ubuntu"),
				"VERSION": measurement.Str("24.04"),
			}),
		),
	)
	target := makeSnapshot(
		makeMeasurement(measurement.TypeOS,
			makeSubtype("release", map[string]measurement.Reading{
				"ID": measurement.Str("ubuntu"),
			}),
		),
	)

	result := Snapshots(baseline, target)
	if result.Summary.Removed != 1 {
		t.Fatalf("expected 1 removed, got %d", result.Summary.Removed)
	}
}

func TestSnapshots_MixedChanges(t *testing.T) {
	baseline := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version":  measurement.Str("1.31.0"),
				"platform": measurement.Str("eks"),
			}),
		),
		makeMeasurement(measurement.TypeSystemD,
			makeSubtype("kubelet", map[string]measurement.Reading{
				"active": measurement.Str("active"),
			}),
		),
	)
	target := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.32.4"),
			}),
		),
		makeMeasurement(measurement.TypeGPU,
			makeSubtype("device", map[string]measurement.Reading{
				"driver": measurement.Str("535.129.03"),
			}),
		),
	)

	result := Snapshots(baseline, target)

	if result.Summary.Modified != 1 {
		t.Errorf("expected 1 modified, got %d", result.Summary.Modified)
	}
	if result.Summary.Removed != 2 {
		t.Errorf("expected 2 removed, got %d", result.Summary.Removed)
	}
	if result.Summary.Added != 1 {
		t.Errorf("expected 1 added, got %d", result.Summary.Added)
	}
}

func TestSnapshots_EmptySnapshots(t *testing.T) {
	result := Snapshots(makeSnapshot(), makeSnapshot())
	if result.HasDrift() {
		t.Errorf("expected no drift for empty snapshots")
	}
}

func TestSnapshots_DeterministicOrder(t *testing.T) {
	baseline := makeSnapshot(
		makeMeasurement(measurement.TypeOS,
			makeSubtype("release", map[string]measurement.Reading{"ID": measurement.Str("ubuntu")}),
		),
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{"version": measurement.Str("1.31.0")}),
		),
	)
	target := makeSnapshot(
		makeMeasurement(measurement.TypeOS,
			makeSubtype("release", map[string]measurement.Reading{"ID": measurement.Str("rhel")}),
		),
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{"version": measurement.Str("1.32.4")}),
		),
	)

	for i := 0; i < 10; i++ {
		result := Snapshots(baseline, target)
		if len(result.Changes) != 2 {
			t.Fatalf("run %d: expected 2 changes, got %d", i, len(result.Changes))
		}
		if result.Changes[0].Path != "K8s.server.version" {
			t.Errorf("run %d: expected K8s.server.version first, got %s", i, result.Changes[0].Path)
		}
		if result.Changes[1].Path != "OS.release.ID" {
			t.Errorf("run %d: expected OS.release.ID second, got %s", i, result.Changes[1].Path)
		}
	}
}

// --- Recipe-vs-Snapshot Tests ---

func TestRecipeVsSnapshot_AllConstraintsPassed(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "K8s.server.version", Value: ">= 1.30", Severity: "error"},
			{Name: "OS.release.ID", Value: "ubuntu", Severity: "error"},
		},
	}

	snap := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.32.4"),
			}),
		),
		makeMeasurement(measurement.TypeOS,
			makeSubtype("release", map[string]measurement.Reading{
				"ID": measurement.Str("ubuntu"),
			}),
		),
	)

	result := RecipeVsSnapshot(rec, snap)

	if result.Mode != "recipe-vs-snapshot" {
		t.Errorf("expected mode recipe-vs-snapshot, got %s", result.Mode)
	}
	if result.Summary.ConstraintsPassed != 2 {
		t.Errorf("expected 2 passed, got %d", result.Summary.ConstraintsPassed)
	}
	if result.Summary.ConstraintsFailed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Summary.ConstraintsFailed)
	}
	if result.HasDrift() {
		t.Errorf("expected no drift when all constraints pass")
	}
}

func TestRecipeVsSnapshot_ConstraintFailed(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "K8s.server.version", Value: ">= 1.32", Severity: "error", Remediation: "Upgrade K8s to 1.32+"},
		},
	}

	snap := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.31.0"),
			}),
		),
	)

	result := RecipeVsSnapshot(rec, snap)

	if result.Summary.ConstraintsFailed != 1 {
		t.Fatalf("expected 1 failed, got %d", result.Summary.ConstraintsFailed)
	}
	if !result.HasDrift() {
		t.Errorf("expected drift when constraint fails")
	}

	cr := result.ConstraintResults[0]
	if cr.Passed {
		t.Errorf("expected constraint to fail")
	}
	if cr.Severity != SeverityError {
		t.Errorf("expected severity error, got %s", cr.Severity)
	}
	if cr.Actual != "1.31.0" {
		t.Errorf("expected actual 1.31.0, got %s", cr.Actual)
	}
	if cr.Remediation != "Upgrade K8s to 1.32+" {
		t.Errorf("expected remediation guidance, got %s", cr.Remediation)
	}
}

func TestRecipeVsSnapshot_WarningSeverity(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "GPU.device.driver", Value: ">= 550.0", Severity: "warning"},
		},
	}

	snap := makeSnapshot(
		makeMeasurement(measurement.TypeGPU,
			makeSubtype("device", map[string]measurement.Reading{
				"driver": measurement.Str("535.129.03"),
			}),
		),
	)

	result := RecipeVsSnapshot(rec, snap)

	if result.Summary.ConstraintsFailed != 1 {
		t.Fatalf("expected 1 failed, got %d", result.Summary.ConstraintsFailed)
	}

	cr := result.ConstraintResults[0]
	if cr.Severity != SeverityWarning {
		t.Errorf("expected severity warning, got %s", cr.Severity)
	}
}

func TestRecipeVsSnapshot_MissingMeasurement(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{
			{Name: "GPU.device.driver", Value: ">= 535.0", Severity: "error"},
		},
	}

	// Snapshot with no GPU measurement
	snap := makeSnapshot(
		makeMeasurement(measurement.TypeK8s,
			makeSubtype("server", map[string]measurement.Reading{
				"version": measurement.Str("1.32.4"),
			}),
		),
	)

	result := RecipeVsSnapshot(rec, snap)

	if result.Summary.ConstraintsError != 1 {
		t.Fatalf("expected 1 error, got %d", result.Summary.ConstraintsError)
	}

	cr := result.ConstraintResults[0]
	if cr.Error == "" {
		t.Errorf("expected error for missing measurement")
	}
}

func TestRecipeVsSnapshot_EmptyConstraints(t *testing.T) {
	rec := &recipe.RecipeResult{
		Constraints: []recipe.Constraint{},
	}
	snap := makeSnapshot()

	result := RecipeVsSnapshot(rec, snap)

	if result.HasDrift() {
		t.Errorf("expected no drift with no constraints")
	}
	if result.Summary.Total != 0 {
		t.Errorf("expected 0 total, got %d", result.Summary.Total)
	}
}
