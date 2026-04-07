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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics shape — see docs/design/005-node-scoped-constraints.md for the
// rationale behind label choices.
//
// The only metric carrying a per-node label is aicr_node_compliant, a gauge
// bounded by fleet size (one series per node). Counters and histograms are
// fleet-aggregated to keep cardinality flat under federation. Per-node
// drill-down for non-compliance is delivered via:
//
//   - the aicr_node_compliant gauge (cluster-wide slice-and-dice)
//   - the aicr.nvidia.com/recipe-compliant node label (set by LabelNode)
//   - rate-limited Kubernetes Events on constraint failures (EmitFailureEvent)
//
// Federation: add external_labels: {cluster: "..."} in Prometheus config for
// cross-cluster aggregation.
var (
	// nodeCompliant is the only metric carrying a {node} label. It reports
	// whether the node is recipe-compliant (1) or not (0). Bounded by fleet
	// size, which is the observable universe anyway. Drop this metric in
	// favor of labels alone if fleet size ever becomes a concern.
	//
	// Primary fleet-wide query:
	//   count(aicr_node_compliant == 0)
	nodeCompliant = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aicr_node_compliant",
			Help: "Whether the node is recipe-compliant (1=compliant, 0=non-compliant)",
		},
		[]string{"node"},
	)

	// nodeValidationTotal counts validation runs across the fleet. No {node}
	// label — per-node attribution lives on the gauge and on K8s Events.
	nodeValidationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aicr_node_validation_total",
			Help: "Total number of node validation runs across the fleet",
		},
		[]string{"result"}, // compliant|non-compliant|error
	)

	// nodeValidationDuration tracks validation latency across the fleet.
	// Fleet-wide histogram keeps cardinality constant under federation.
	nodeValidationDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "aicr_node_validation_duration_seconds",
			Help:    "Duration of per-node recipe validation across the fleet",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60},
		},
	)

	// nodeConstraintsFailedTotal counts constraint failures across the fleet
	// for alerting on drift trends. No {node} or {constraint} labels — those
	// would explode cardinality. Use the gauge and Events for drill-down.
	nodeConstraintsFailedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "aicr_node_constraints_failed_total",
			Help: "Total number of constraint failures observed across all node validation runs",
		},
	)
)

// RecordNodeMetrics updates Prometheus metrics from a node validation result.
// Called after each validation run (both one-shot and loop mode).
//
// Only the aicr_node_compliant gauge carries per-node detail. Counters and
// histograms are fleet-aggregated.
func RecordNodeMetrics(result *NodeResult, durationSeconds float64) {
	node := result.NodeName

	// Compliance gauge — the only per-node metric.
	if result.Compliant {
		nodeCompliant.WithLabelValues(node).Set(1)
	} else {
		nodeCompliant.WithLabelValues(node).Set(0)
	}

	// Fleet-wide duration histogram.
	nodeValidationDuration.Observe(durationSeconds)

	// Fleet-wide run counter.
	runResult := "compliant"
	if !result.Compliant {
		runResult = "non-compliant"
	}
	nodeValidationTotal.WithLabelValues(runResult).Inc()

	// Fleet-wide constraint-failure counter.
	if result.DiffResult != nil {
		nodeConstraintsFailedTotal.Add(float64(result.DiffResult.Summary.ConstraintsFailed))
	}
}
