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

// Package diff compares AICR snapshots and evaluates them against recipe
// constraints to detect configuration drift. It supports two modes:
//
//   - Snapshot-vs-snapshot: raw field-level comparison between two states.
//   - Recipe-vs-snapshot: evaluate recipe constraints against a snapshot to
//     detect drift from the recipe-defined desired state.
package diff

import (
	"sort"

	"github.com/NVIDIA/aicr/pkg/constraints"
	"github.com/NVIDIA/aicr/pkg/measurement"
	"github.com/NVIDIA/aicr/pkg/recipe"
	"github.com/NVIDIA/aicr/pkg/snapshotter"
)

// ChangeKind describes the type of difference detected.
type ChangeKind string

const (
	// Added indicates a value exists in the target but not the baseline.
	Added ChangeKind = "added"
	// Removed indicates a value exists in the baseline but not the target.
	Removed ChangeKind = "removed"
	// Modified indicates a value changed between baseline and target.
	Modified ChangeKind = "modified"
)

// Severity classifies the impact of a detected change.
type Severity string

const (
	// SeverityInfo indicates an informational change that does not violate constraints.
	SeverityInfo Severity = "info"
	// SeverityWarning indicates the change violates a warning-level constraint.
	SeverityWarning Severity = "warning"
	// SeverityError indicates the change violates an error-level constraint.
	SeverityError Severity = "error"
)

// Change represents a single difference between two snapshots.
type Change struct {
	// Kind is the type of change (added, removed, modified).
	Kind ChangeKind `json:"kind" yaml:"kind"`
	// Severity classifies the impact (info, warning, error).
	Severity Severity `json:"severity" yaml:"severity"`
	// Path is the dot-separated location (e.g., "K8s.server.version").
	Path string `json:"path" yaml:"path"`
	// Baseline is the value in the baseline snapshot (empty for Added).
	Baseline string `json:"baseline,omitempty" yaml:"baseline,omitempty"`
	// Target is the value in the target snapshot (empty for Removed).
	Target string `json:"target,omitempty" yaml:"target,omitempty"`
}

// ConstraintResult represents the evaluation of a single recipe constraint against a snapshot.
type ConstraintResult struct {
	// Name is the constraint path (e.g., "K8s.server.version").
	Name string `json:"name" yaml:"name"`
	// Expected is the constraint expression (e.g., ">= 1.30").
	Expected string `json:"expected" yaml:"expected"`
	// Actual is the value found in the snapshot.
	Actual string `json:"actual" yaml:"actual"`
	// Passed indicates if the constraint is satisfied.
	Passed bool `json:"passed" yaml:"passed"`
	// Severity is the constraint severity from the recipe.
	Severity Severity `json:"severity" yaml:"severity"`
	// Remediation is guidance from the recipe for fixing violations.
	Remediation string `json:"remediation,omitempty" yaml:"remediation,omitempty"`
	// Error is the evaluation error if any.
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
}

// Result contains the complete diff output.
type Result struct {
	// Mode describes the comparison mode ("snapshot-vs-snapshot" or "recipe-vs-snapshot").
	Mode string `json:"mode" yaml:"mode"`
	// BaselineSource identifies the baseline (file path, recipe name, etc.).
	BaselineSource string `json:"baselineSource,omitempty" yaml:"baselineSource,omitempty"`
	// TargetSource identifies the target snapshot.
	TargetSource string `json:"targetSource,omitempty" yaml:"targetSource,omitempty"`
	// Changes is the list of field-level differences (snapshot-vs-snapshot mode).
	Changes []Change `json:"changes,omitempty" yaml:"changes,omitempty"`
	// ConstraintResults is the list of constraint evaluations (recipe-vs-snapshot mode).
	ConstraintResults []ConstraintResult `json:"constraintResults,omitempty" yaml:"constraintResults,omitempty"`
	// Summary contains aggregate counts.
	Summary Summary `json:"summary" yaml:"summary"`
}

// Summary provides aggregate counts.
type Summary struct {
	Added    int `json:"added" yaml:"added"`
	Removed  int `json:"removed" yaml:"removed"`
	Modified int `json:"modified" yaml:"modified"`
	Total    int `json:"total" yaml:"total"`
	// Constraint-specific counts (recipe-vs-snapshot mode).
	ConstraintsPassed int `json:"constraintsPassed,omitempty" yaml:"constraintsPassed,omitempty"`
	ConstraintsFailed int `json:"constraintsFailed,omitempty" yaml:"constraintsFailed,omitempty"`
	ConstraintsError  int `json:"constraintsError,omitempty" yaml:"constraintsError,omitempty"`
}

// HasDrift returns true if any field-level changes or constraint violations were detected.
func (r *Result) HasDrift() bool {
	if r.Mode == "recipe-vs-snapshot" {
		return r.Summary.ConstraintsFailed > 0 || r.Summary.ConstraintsError > 0
	}
	return r.Summary.Total > 0
}

// Snapshots compares two snapshots and returns a structured diff result.
// The baseline is the reference state; the target is the current state.
func Snapshots(baseline, target *snapshotter.Snapshot) *Result {
	result := &Result{
		Mode:    "snapshot-vs-snapshot",
		Changes: make([]Change, 0),
	}

	if baseline.Metadata != nil {
		if src, ok := baseline.Metadata["source-node"]; ok {
			result.BaselineSource = src
		}
	}
	if target.Metadata != nil {
		if src, ok := target.Metadata["source-node"]; ok {
			result.TargetSource = src
		}
	}

	baseByType := indexMeasurements(baseline.Measurements)
	targetByType := indexMeasurements(target.Measurements)

	allTypes := mergeKeys(baseByType, targetByType)
	sort.Strings(allTypes)

	for _, typeName := range allTypes {
		baseMeasurement, baseExists := baseByType[typeName]
		targetMeasurement, targetExists := targetByType[typeName]

		if !baseExists {
			result.Changes = append(result.Changes, addedMeasurement(targetMeasurement)...)
			continue
		}
		if !targetExists {
			result.Changes = append(result.Changes, removedMeasurement(baseMeasurement)...)
			continue
		}

		result.Changes = append(result.Changes, compareMeasurements(baseMeasurement, targetMeasurement)...)
	}

	sort.Slice(result.Changes, func(i, j int) bool {
		return result.Changes[i].Path < result.Changes[j].Path
	})

	for _, c := range result.Changes {
		switch c.Kind {
		case Added:
			result.Summary.Added++
		case Removed:
			result.Summary.Removed++
		case Modified:
			result.Summary.Modified++
		}
	}
	result.Summary.Total = len(result.Changes)

	return result
}

// RecipeVsSnapshot evaluates a recipe's constraints against a snapshot to detect
// drift from the recipe-defined desired state. This is the primary drift detection
// mode — it answers "does this cluster still match what the recipe requires?"
func RecipeVsSnapshot(rec *recipe.RecipeResult, snap *snapshotter.Snapshot) *Result {
	result := &Result{
		Mode:              "recipe-vs-snapshot",
		ConstraintResults: make([]ConstraintResult, 0, len(rec.Constraints)),
	}

	for _, constraint := range rec.Constraints {
		cr := evaluateConstraint(constraint, snap)
		result.ConstraintResults = append(result.ConstraintResults, cr)

		if cr.Error != "" {
			result.Summary.ConstraintsError++
		} else if cr.Passed {
			result.Summary.ConstraintsPassed++
		} else {
			result.Summary.ConstraintsFailed++
		}
	}

	// Sort by name for deterministic output
	sort.Slice(result.ConstraintResults, func(i, j int) bool {
		return result.ConstraintResults[i].Name < result.ConstraintResults[j].Name
	})

	result.Summary.Total = len(result.ConstraintResults)

	return result
}

// evaluateConstraint evaluates a single recipe constraint against a snapshot.
func evaluateConstraint(c recipe.Constraint, snap *snapshotter.Snapshot) ConstraintResult {
	cr := ConstraintResult{
		Name:        c.Name,
		Expected:    c.Value,
		Remediation: c.Remediation,
	}

	// Map recipe severity to diff severity
	switch c.Severity {
	case "warning":
		cr.Severity = SeverityWarning
	default:
		cr.Severity = SeverityError
	}

	eval := constraints.Evaluate(c, snap)
	if eval.Error != nil {
		cr.Error = eval.Error.Error()
		cr.Actual = eval.Actual
		return cr
	}

	cr.Actual = eval.Actual
	cr.Passed = eval.Passed
	return cr
}

// --- snapshot-vs-snapshot helpers ---

func indexMeasurements(measurements []*measurement.Measurement) map[string]*measurement.Measurement {
	idx := make(map[string]*measurement.Measurement, len(measurements))
	for _, m := range measurements {
		idx[string(m.Type)] = m
	}
	return idx
}

func compareMeasurements(base, target *measurement.Measurement) []Change {
	var changes []Change

	baseByName := indexSubtypes(base.Subtypes)
	targetByName := indexSubtypes(target.Subtypes)

	allNames := mergeKeys(baseByName, targetByName)
	sort.Strings(allNames)

	for _, name := range allNames {
		baseSt, baseExists := baseByName[name]
		targetSt, targetExists := targetByName[name]

		prefix := string(base.Type) + "." + name

		if !baseExists {
			changes = append(changes, addedSubtype(prefix, targetSt)...)
			continue
		}
		if !targetExists {
			changes = append(changes, removedSubtype(prefix, baseSt)...)
			continue
		}

		changes = append(changes, compareReadings(prefix, baseSt.Data, targetSt.Data)...)
	}

	return changes
}

func compareReadings(prefix string, base, target map[string]measurement.Reading) []Change {
	var changes []Change

	allKeys := mergeReadingKeys(base, target)
	sort.Strings(allKeys)

	for _, key := range allKeys {
		path := prefix + "." + key
		baseReading, baseExists := base[key]
		targetReading, targetExists := target[key]

		if !baseExists {
			changes = append(changes, Change{Kind: Added, Severity: SeverityInfo, Path: path, Target: targetReading.String()})
			continue
		}
		if !targetExists {
			changes = append(changes, Change{Kind: Removed, Severity: SeverityInfo, Path: path, Baseline: baseReading.String()})
			continue
		}

		baseVal := baseReading.String()
		targetVal := targetReading.String()
		if baseVal != targetVal {
			changes = append(changes, Change{Kind: Modified, Severity: SeverityInfo, Path: path, Baseline: baseVal, Target: targetVal})
		}
	}

	return changes
}

func addedMeasurement(m *measurement.Measurement) []Change {
	var changes []Change
	for _, st := range m.Subtypes {
		prefix := string(m.Type) + "." + st.Name
		changes = append(changes, addedSubtype(prefix, &st)...)
	}
	return changes
}

func removedMeasurement(m *measurement.Measurement) []Change {
	var changes []Change
	for _, st := range m.Subtypes {
		prefix := string(m.Type) + "." + st.Name
		changes = append(changes, removedSubtype(prefix, &st)...)
	}
	return changes
}

func addedSubtype(prefix string, st *measurement.Subtype) []Change {
	changes := make([]Change, 0, len(st.Data))
	for key, reading := range st.Data {
		changes = append(changes, Change{Kind: Added, Severity: SeverityInfo, Path: prefix + "." + key, Target: reading.String()})
	}
	return changes
}

func removedSubtype(prefix string, st *measurement.Subtype) []Change {
	changes := make([]Change, 0, len(st.Data))
	for key, reading := range st.Data {
		changes = append(changes, Change{Kind: Removed, Severity: SeverityInfo, Path: prefix + "." + key, Baseline: reading.String()})
	}
	return changes
}

func indexSubtypes(subtypes []measurement.Subtype) map[string]*measurement.Subtype {
	idx := make(map[string]*measurement.Subtype, len(subtypes))
	for i := range subtypes {
		idx[subtypes[i].Name] = &subtypes[i]
	}
	return idx
}

func mergeKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

func mergeReadingKeys(a, b map[string]measurement.Reading) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}
