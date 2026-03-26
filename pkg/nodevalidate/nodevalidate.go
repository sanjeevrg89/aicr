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

// Package nodevalidate provides per-node recipe compliance validation.
// It runs locally on a GPU node, captures a snapshot using the existing
// collector framework, evaluates recipe constraints, and reports compliance.
// Designed for DaemonSet or init container execution alongside Skyhook.
package nodevalidate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/aicr/pkg/collector"
	"github.com/NVIDIA/aicr/pkg/collector/k8s"
	"github.com/NVIDIA/aicr/pkg/diff"
	"github.com/NVIDIA/aicr/pkg/errors"
	"github.com/NVIDIA/aicr/pkg/header"
	k8sclient "github.com/NVIDIA/aicr/pkg/k8s/client"
	"github.com/NVIDIA/aicr/pkg/recipe"
	"github.com/NVIDIA/aicr/pkg/snapshotter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// LabelCompliance is the node label set by node-validate to indicate
	// recipe compliance status.
	LabelCompliance = "aicr.nvidia.com/recipe-compliant"

	// LabelLastValidated records the timestamp of the last validation.
	LabelLastValidated = "aicr.nvidia.com/last-validated"

	// snapshotAPIVersion is used for locally-collected snapshots.
	snapshotAPIVersion = "aicr.nvidia.com/v1alpha1"
)

// NodeResult contains the outcome of a per-node validation.
type NodeResult struct {
	// NodeName is the name of the validated node.
	NodeName string `json:"nodeName" yaml:"nodeName"`
	// Compliant is true if all recipe constraints pass on this node.
	Compliant bool `json:"compliant" yaml:"compliant"`
	// Timestamp is when the validation was performed.
	Timestamp string `json:"timestamp" yaml:"timestamp"`
	// DiffResult contains the full constraint evaluation from diff.RecipeVsSnapshot.
	DiffResult *diff.Result `json:"diffResult" yaml:"diffResult"`
}

// Config holds configuration for node validation.
type Config struct {
	// Version is the AICR CLI version (for snapshot metadata).
	Version string
	// Factory is the collector factory. If nil, uses DefaultFactory.
	Factory collector.Factory
}

// ValidateNode runs collectors locally and evaluates recipe constraints against
// the local node's state. This reuses the same collector framework as the
// snapshot agent (pkg/snapshotter) and the same constraint evaluation as
// aicr diff and validator.checkReadiness (pkg/constraints).
func ValidateNode(ctx context.Context, rec *recipe.RecipeResult, cfg Config) (*NodeResult, error) {
	start := time.Now()

	nodeName := k8s.GetNodeName()
	slog.Info("starting node validation", slog.String("node", nodeName))

	// Collect local node snapshot
	snap, err := collectLocalSnapshot(ctx, nodeName, cfg)
	if err != nil {
		return nil, errors.Wrap(errors.ErrCodeInternal, "failed to collect local snapshot", err)
	}

	// Evaluate recipe constraints against local snapshot.
	// This is the same code path as `aicr diff --recipe` and validator.checkReadiness.
	result := diff.RecipeVsSnapshot(rec, snap)
	result.BaselineSource = "recipe"
	result.TargetSource = fmt.Sprintf("node:%s", nodeName)

	nodeResult := &NodeResult{
		NodeName:   nodeName,
		Compliant:  !result.HasDrift(),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		DiffResult: result,
	}

	slog.Info("node validation complete",
		slog.String("node", nodeName),
		slog.Bool("compliant", nodeResult.Compliant),
		slog.Int("constraintsPassed", result.Summary.ConstraintsPassed),
		slog.Int("constraintsFailed", result.Summary.ConstraintsFailed),
		slog.Duration("duration", time.Since(start)))

	return nodeResult, nil
}

// LabelNode applies compliance labels to the node via Kubernetes API.
// Uses the provided clientset to avoid creating a new client on each call.
// Requires RBAC permission to patch nodes (verb: patch, resource: nodes).
func LabelNode(ctx context.Context, clientset k8sclient.Interface, result *NodeResult) error {
	if result.NodeName == "" {
		return errors.New(errors.ErrCodeInvalidRequest, "node name is empty — cannot label")
	}

	labels := result.LabelValues()

	// Build strategic merge patch for labels
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": labels,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal, "failed to marshal label patch", err)
	}

	_, err = clientset.CoreV1().Nodes().Patch(
		ctx,
		result.NodeName,
		types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal,
			fmt.Sprintf("failed to patch node %q labels", result.NodeName), err)
	}

	slog.Info("node labels applied",
		slog.String("node", result.NodeName),
		slog.String(LabelCompliance, labels[LabelCompliance]))

	return nil
}

// RunLoop runs node validation repeatedly at the given interval until the context
// is cancelled. Designed for DaemonSet execution where the container must stay alive.
// On each iteration it validates, labels the node, and sleeps for the interval.
func RunLoop(ctx context.Context, rec *recipe.RecipeResult, cfg Config, interval time.Duration, clientset k8sclient.Interface) error {
	slog.Info("starting node-validate loop", slog.Duration("interval", interval))

	for {
		result, err := ValidateNode(ctx, rec, cfg)
		if err != nil {
			slog.Error("validation iteration failed", slog.String("error", err.Error()))
		} else if clientset != nil {
			if labelErr := LabelNode(ctx, clientset, result); labelErr != nil {
				slog.Warn("failed to label node", slog.String("error", labelErr.Error()))
			}
		}

		select {
		case <-ctx.Done():
			slog.Info("node-validate loop stopped")
			return nil
		case <-time.After(interval):
			// next iteration
		}
	}
}

// collectLocalSnapshot runs collectors locally (same pattern as NodeSnapshotter.measure)
// and returns a snapshot of the current node's state.
func collectLocalSnapshot(ctx context.Context, nodeName string, cfg Config) (*snapshotter.Snapshot, error) {
	factory := cfg.Factory
	if factory == nil {
		factory = collector.NewDefaultFactory()
	}

	snap := snapshotter.NewSnapshot()
	snap.Init(header.KindSnapshot, snapshotAPIVersion, cfg.Version)
	snap.Metadata["source-node"] = nodeName

	var mu sync.Mutex

	collectSafe := func(name string, c collector.Collector) func() error {
		return func() error {
			m, err := c.Collect(ctx)
			if err != nil {
				slog.Warn("collector failed — skipping",
					slog.String("collector", name),
					slog.String("error", err.Error()))
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			snap.Measurements = append(snap.Measurements, m)
			return nil
		}
	}

	g, _ := errgroup.WithContext(ctx)
	g.Go(collectSafe("k8s", factory.CreateKubernetesCollector()))
	g.Go(collectSafe("gpu", factory.CreateGPUCollector()))
	g.Go(collectSafe("os", factory.CreateOSCollector()))
	g.Go(collectSafe("systemd", factory.CreateSystemDCollector()))

	_ = g.Wait()

	slog.Debug("local snapshot collected", slog.Int("measurements", len(snap.Measurements)))
	return snap, nil
}

// LabelValues returns the node labels that should be applied based on the validation result.
func (r *NodeResult) LabelValues() map[string]string {
	compliant := "false"
	if r.Compliant {
		compliant = "true"
	}
	return map[string]string{
		LabelCompliance:    compliant,
		LabelLastValidated: r.Timestamp,
	}
}

// FailedConstraints returns only the constraints that failed evaluation.
func (r *NodeResult) FailedConstraints() []diff.ConstraintResult {
	var failed []diff.ConstraintResult
	for _, cr := range r.DiffResult.ConstraintResults {
		if !cr.Passed && cr.Error == "" {
			failed = append(failed, cr)
		}
	}
	return failed
}

// ErrorConstraints returns constraints that could not be evaluated.
func (r *NodeResult) ErrorConstraints() []diff.ConstraintResult {
	var errs []diff.ConstraintResult
	for _, cr := range r.DiffResult.ConstraintResults {
		if cr.Error != "" {
			errs = append(errs, cr)
		}
	}
	return errs
}
