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

var (
	// nodeCompliant reports whether the node is recipe-compliant (1) or not (0).
	// This is the primary metric for fleet-wide observability — a Prometheus
	// query like `count(aicr_node_compliant == 0)` shows how many nodes are
	// non-compliant across the fleet.
	nodeCompliant = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aicr_node_compliant",
			Help: "Whether the node is recipe-compliant (1=compliant, 0=non-compliant)",
		},
		[]string{"node"},
	)

	// nodeConstraintsPassed reports the number of constraints that passed
	// on the last validation for this node.
	nodeConstraintsPassed = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aicr_node_constraints_passed",
			Help: "Number of recipe constraints that passed on this node",
		},
		[]string{"node"},
	)

	// nodeConstraintsFailed reports the number of constraints that failed
	// on the last validation for this node.
	nodeConstraintsFailed = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aicr_node_constraints_failed",
			Help: "Number of recipe constraints that failed on this node",
		},
		[]string{"node"},
	)

	// nodeValidationDuration tracks per-node validation latency.
	nodeValidationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aicr_node_validation_duration_seconds",
			Help:    "Duration of per-node recipe validation",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60},
		},
		[]string{"node"},
	)

	// nodeValidationTotal counts validation runs per node.
	nodeValidationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aicr_node_validation_total",
			Help: "Total number of node validation runs",
		},
		[]string{"node", "result"}, // result: compliant|non-compliant|error
	)
)

// RecordNodeMetrics updates Prometheus metrics from a node validation result.
// Called after each validation run (both one-shot and loop mode).
func RecordNodeMetrics(result *NodeResult, durationSeconds float64) {
	node := result.NodeName

	// Compliance gauge
	if result.Compliant {
		nodeCompliant.WithLabelValues(node).Set(1)
	} else {
		nodeCompliant.WithLabelValues(node).Set(0)
	}

	// Constraint counts
	nodeConstraintsPassed.WithLabelValues(node).Set(float64(result.DiffResult.Summary.ConstraintsPassed))
	nodeConstraintsFailed.WithLabelValues(node).Set(float64(result.DiffResult.Summary.ConstraintsFailed))

	// Duration
	nodeValidationDuration.WithLabelValues(node).Observe(durationSeconds)

	// Run counter
	runResult := "compliant"
	if !result.Compliant {
		runResult = "non-compliant"
	}
	nodeValidationTotal.WithLabelValues(node, runResult).Inc()
}
