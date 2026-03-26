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
	"testing"

	"github.com/NVIDIA/aicr/pkg/diff"
	dto "github.com/prometheus/client_model/go"
)

func gaugeVal(t *testing.T, g interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("failed to read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestRecordNodeMetrics_Compliant(t *testing.T) {
	result := &NodeResult{
		NodeName:  "metrics-node-1",
		Compliant: true,
		DiffResult: &diff.Result{
			Mode:    "recipe-vs-snapshot",
			Summary: diff.Summary{ConstraintsPassed: 3, ConstraintsFailed: 0},
		},
	}

	RecordNodeMetrics(result, 1.5)

	if v := gaugeVal(t, nodeCompliant.WithLabelValues("metrics-node-1")); v != 1 {
		t.Errorf("expected compliant=1, got %f", v)
	}
	if v := gaugeVal(t, nodeConstraintsPassed.WithLabelValues("metrics-node-1")); v != 3 {
		t.Errorf("expected 3 passed, got %f", v)
	}
	if v := gaugeVal(t, nodeConstraintsFailed.WithLabelValues("metrics-node-1")); v != 0 {
		t.Errorf("expected 0 failed, got %f", v)
	}
}

func TestRecordNodeMetrics_NonCompliant(t *testing.T) {
	result := &NodeResult{
		NodeName:  "metrics-node-2",
		Compliant: false,
		DiffResult: &diff.Result{
			Mode:    "recipe-vs-snapshot",
			Summary: diff.Summary{ConstraintsPassed: 1, ConstraintsFailed: 2},
		},
	}

	RecordNodeMetrics(result, 2.0)

	if v := gaugeVal(t, nodeCompliant.WithLabelValues("metrics-node-2")); v != 0 {
		t.Errorf("expected compliant=0, got %f", v)
	}
	if v := gaugeVal(t, nodeConstraintsFailed.WithLabelValues("metrics-node-2")); v != 2 {
		t.Errorf("expected 2 failed, got %f", v)
	}
}
