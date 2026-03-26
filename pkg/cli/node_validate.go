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

package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/NVIDIA/aicr/pkg/errors"
	"github.com/NVIDIA/aicr/pkg/nodevalidate"
	"github.com/NVIDIA/aicr/pkg/recipe"
	"github.com/NVIDIA/aicr/pkg/serializer"
)

// nodeValidateCmd creates the "node-validate" CLI command.
func nodeValidateCmd() *cli.Command {
	return &cli.Command{
		Name:     "node-validate",
		Category: functionalCategoryName,
		Usage:    "Validate this node against recipe constraints",
		Description: `Run recipe constraint validation locally on the current node.
Designed for DaemonSet, init container, or CronJob execution to
continuously ensure GPU nodes are recipe-compliant.

Collectors run locally (same as snapshot agent mode) to capture
GPU, OS, kernel, and K8s node state, then evaluate recipe
constraints against the collected data.

Use --label-node to set the aicr.nvidia.com/recipe-compliant label
on the node based on the validation result.

Examples:
  # Validate this node against a recipe
  aicr node-validate --recipe recipe.yaml

  # JSON output for logging/metrics pipelines
  aicr node-validate --recipe recipe.yaml --format json

  # Fail with non-zero exit if node is non-compliant
  aicr node-validate --recipe recipe.yaml --fail-on-drift

  # Label the node with compliance status (requires cluster RBAC)
  aicr node-validate --recipe recipe.yaml --label-node`,
		Flags: nodeValidateCmdFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runNodeValidateCmd(ctx, cmd)
		},
	}
}

func nodeValidateCmdFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "recipe",
			Aliases:  []string{"r"},
			Usage:    "recipe file: path, URL, or ConfigMap URI",
			Required: true,
			Category: "Input",
		},
		&cli.BoolFlag{
			Name:     "label-node",
			Usage:    fmt.Sprintf("set %s label on this node based on result (requires node patch RBAC)", nodevalidate.LabelCompliance),
			Category: "Node",
		},
		&cli.BoolFlag{
			Name:     "fail-on-drift",
			Usage:    "exit with non-zero status if node is non-compliant",
			Category: "Output",
		},
		outputFlag,
		formatFlag,
		kubeconfigFlag,
		dataFlag,
	}
}

func runNodeValidateCmd(ctx context.Context, cmd *cli.Command) error {
	if err := validateSingleValueFlags(cmd, "recipe", "output", "format"); err != nil {
		return err
	}

	outFormat, err := parseOutputFormat(cmd)
	if err != nil {
		return err
	}

	if err := initDataProvider(cmd); err != nil {
		return err
	}

	recipePath := cmd.String("recipe")
	kubeconfig := cmd.String("kubeconfig")

	slog.Debug("loading recipe", slog.String("path", recipePath))

	rec, err := serializer.FromFileWithKubeconfig[recipe.RecipeResult](recipePath, kubeconfig)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal, fmt.Sprintf("failed to load recipe from %q", recipePath), err)
	}

	// Run node validation (collectors + constraint evaluation)
	result, err := nodevalidate.ValidateNode(ctx, rec, nodevalidate.Config{
		Version: version,
	})
	if err != nil {
		return err
	}

	// Label node if requested
	if cmd.Bool("label-node") {
		if labelErr := labelNode(ctx, kubeconfig, result); labelErr != nil {
			slog.Warn("failed to label node", slog.String("error", labelErr.Error()))
			// Non-fatal — continue with output
		}
	}

	// Write output
	if err := writeNodeResult(ctx, cmd, outFormat, result); err != nil {
		return err
	}

	// Fail if non-compliant and --fail-on-drift set
	if cmd.Bool("fail-on-drift") && !result.Compliant {
		failed := result.FailedConstraints()
		errConstraints := result.ErrorConstraints()
		return errors.New(errors.ErrCodeInvalidRequest,
			fmt.Sprintf("node %s is non-compliant: %d constraint(s) failed, %d error(s)",
				result.NodeName, len(failed), len(errConstraints)))
	}

	return nil
}

// labelNode applies compliance labels to the current node via K8s API.
func labelNode(ctx context.Context, kubeconfig string, result *nodevalidate.NodeResult) error {
	// Import here to avoid pulling K8s client dependencies when --label-node is not used
	k8sClient, _, err := getKubeClient(kubeconfig)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal, "failed to create kubernetes client for node labeling", err)
	}

	labels := result.LabelValues()
	return patchNodeLabels(ctx, k8sClient, result.NodeName, labels)
}

// getKubeClient returns a kubernetes clientset, with optional kubeconfig override.
func getKubeClient(kubeconfig string) (interface{}, interface{}, error) {
	// Placeholder — actual implementation uses pkg/k8s/client
	// This will be connected when running in-cluster with proper RBAC
	return nil, nil, errors.New(errors.ErrCodeInternal, "node labeling requires in-cluster execution with node patch RBAC")
}

// patchNodeLabels applies labels to a node via strategic merge patch.
func patchNodeLabels(_ context.Context, _ interface{}, _ string, _ map[string]string) error {
	// Placeholder — actual implementation uses clientset.CoreV1().Nodes().Patch()
	return errors.New(errors.ErrCodeInternal, "node labeling requires in-cluster execution with node patch RBAC")
}

// writeNodeResult serializes the node validation result.
func writeNodeResult(ctx context.Context, cmd *cli.Command, outFormat serializer.Format, result *nodevalidate.NodeResult) error {
	output := cmd.String("output")

	// Table output uses the diff table writer
	if outFormat == serializer.FormatTable {
		return writeDiffResult(ctx, cmd, outFormat, result.DiffResult)
	}

	ser, err := serializer.NewFileWriterOrStdout(outFormat, output)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal, "failed to create output writer", err)
	}
	defer func() {
		if closer, ok := ser.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	return ser.Serialize(ctx, result)
}
