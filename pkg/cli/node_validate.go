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
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"

	"github.com/NVIDIA/aicr/pkg/diff"
	"github.com/NVIDIA/aicr/pkg/errors"
	k8sclient "github.com/NVIDIA/aicr/pkg/k8s/client"
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
on the node based on the validation result. Requires RBAC permission
to patch nodes.

Use --interval to run continuously in a loop (for DaemonSet mode).
The container stays alive and re-validates at each interval.

Examples:
  # One-shot: validate this node against a recipe
  aicr node-validate --recipe recipe.yaml

  # Continuous: validate every 5 minutes and label the node (DaemonSet mode)
  aicr node-validate --recipe recipe.yaml --label-node --interval 5m

  # JSON output for logging/metrics pipelines
  aicr node-validate --recipe recipe.yaml --format json

  # Fail with non-zero exit if node is non-compliant (init container mode)
  aicr node-validate --recipe recipe.yaml --fail-on-drift`,
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
		&cli.DurationFlag{
			Name:     "interval",
			Usage:    "run continuously at this interval (e.g., 5m, 1h). Without this flag, runs once and exits.",
			Category: "Node",
		},
		&cli.IntFlag{
			Name:     "metrics-port",
			Usage:    "expose Prometheus /metrics endpoint on this port (used with --interval for DaemonSet mode)",
			Category: "Node",
		},
		&cli.BoolFlag{
			Name:     "fail-on-drift",
			Usage:    "exit with non-zero status if node is non-compliant (one-shot mode only)",
			Category: "Output",
		},
		outputFlag,
		formatFlag,
		kubeconfigFlag,
		dataFlag,
	}
}

func runNodeValidateCmd(ctx context.Context, cmd *cli.Command) error {
	if err := validateSingleValueFlags(cmd, "recipe", "output", "format", "interval"); err != nil {
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
	interval := cmd.Duration("interval")

	rec, err := serializer.FromFileWithKubeconfig[recipe.RecipeResult](recipePath, kubeconfig)
	if err != nil {
		return errors.Wrap(errors.ErrCodeInternal, fmt.Sprintf("failed to load recipe from %q", recipePath), err)
	}

	cfg := nodevalidate.Config{Version: version}

	// Loop mode: run continuously for DaemonSet execution
	if interval > 0 {
		if cmd.Bool("fail-on-drift") {
			return errors.New(errors.ErrCodeInvalidRequest, "--fail-on-drift cannot be used with --interval (loop never exits on drift)")
		}

		var clientset k8sclient.Interface
		if cmd.Bool("label-node") {
			cs, csErr := getNodeValidateClient(kubeconfig)
			if csErr != nil {
				return csErr
			}
			clientset = cs
		}

		// Start Prometheus metrics server if --metrics-port is set
		if metricsPort := cmd.Int("metrics-port"); metricsPort > 0 {
			mux := http.NewServeMux()
			mux.Handle("/metrics", promhttp.Handler())
			mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			addr := fmt.Sprintf(":%d", metricsPort)
			go func() {
				slog.Info("starting metrics server", slog.String("addr", addr))
				if srvErr := http.ListenAndServe(addr, mux); srvErr != nil && srvErr != http.ErrServerClosed { //nolint:gosec // Metrics server is internal, no TLS needed
					slog.Error("metrics server failed", slog.String("error", srvErr.Error()))
				}
			}()
		}

		return nodevalidate.RunLoop(ctx, rec, cfg, interval, clientset)
	}

	// One-shot mode: validate once and exit
	result, err := nodevalidate.ValidateNode(ctx, rec, cfg)
	if err != nil {
		return err
	}

	if cmd.Bool("label-node") {
		clientset, csErr := getNodeValidateClient(kubeconfig)
		if csErr != nil {
			slog.Warn("failed to create kubernetes client for labeling", slog.String("error", csErr.Error()))
		} else {
			if labelErr := nodevalidate.LabelNode(ctx, clientset, result); labelErr != nil {
				slog.Warn("failed to label node — continuing with output",
					slog.String("error", labelErr.Error()))
			}
		}
	}

	if err := writeNodeValidateResult(ctx, cmd, outFormat, result); err != nil {
		return err
	}

	if cmd.Bool("fail-on-drift") && !result.Compliant {
		failed := result.FailedConstraints()
		errConstraints := result.ErrorConstraints()
		return errors.New(errors.ErrCodeInvalidRequest,
			fmt.Sprintf("node %s is non-compliant: %d constraint(s) failed, %d error(s)",
				result.NodeName, len(failed), len(errConstraints)))
	}

	return nil
}

// getNodeValidateClient returns a K8s clientset for node labeling.
// Uses the singleton GetKubeClient by default; uses BuildKubeClient
// for explicit kubeconfig to avoid polluting the singleton cache.
func getNodeValidateClient(kubeconfig string) (k8sclient.Interface, error) {
	if kubeconfig != "" {
		cs, _, err := k8sclient.GetKubeClientWithConfig(kubeconfig)
		return cs, err
	}
	cs, _, err := k8sclient.GetKubeClient()
	return cs, err
}

// writeNodeValidateResult serializes the node validation result.
func writeNodeValidateResult(ctx context.Context, cmd *cli.Command, outFormat serializer.Format, result *nodevalidate.NodeResult) error {
	output := cmd.String("output")

	if outFormat == serializer.FormatTable {
		w := os.Stdout
		if output != "" {
			f, err := os.Create(output)
			if err != nil {
				return errors.Wrap(errors.ErrCodeInternal, "failed to create output file", err)
			}
			defer func() {
				if closeErr := f.Close(); closeErr != nil {
					slog.Warn("failed to close output file", slog.String("error", closeErr.Error()))
				}
			}()
			w = f
		}
		fmt.Fprintf(w, "NODE: %s\n", result.NodeName)
		if result.Compliant {
			fmt.Fprintln(w, "STATUS: COMPLIANT")
		} else {
			fmt.Fprintln(w, "STATUS: NON-COMPLIANT")
		}
		fmt.Fprintf(w, "TIMESTAMP: %s\n\n", result.Timestamp)
		return diff.WriteTable(w, result.DiffResult)
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
