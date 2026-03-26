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
	"testing"

	"github.com/urfave/cli/v3"
)

func TestNodeValidateCmd_CommandStructure(t *testing.T) {
	cmd := nodeValidateCmd()

	if cmd.Name != "node-validate" {
		t.Errorf("command name = %q, want %q", cmd.Name, "node-validate")
	}
	if cmd.Category != functionalCategoryName {
		t.Errorf("category = %q, want %q", cmd.Category, functionalCategoryName)
	}
}

func TestNodeValidateCmd_RequiredFlags(t *testing.T) {
	cmd := nodeValidateCmd()

	requiredFlags := []string{"recipe", "fail-on-drift", "label-node", "output", "format", "kubeconfig", "data"}
	for _, flagName := range requiredFlags {
		found := false
		for _, flag := range cmd.Flags {
			if hasFlag(flag, flagName) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing flag: %s", flagName)
		}
	}
}

func TestNodeValidateCmd_RecipeAlias(t *testing.T) {
	cmd := nodeValidateCmd()
	for _, flag := range cmd.Flags {
		if hasFlag(flag, "recipe") && !hasFlag(flag, "r") {
			t.Error("--recipe flag missing -r alias")
		}
	}
}

func TestNodeValidateCmd_MissingRecipe(t *testing.T) {
	cmd := nodeValidateCmd()
	app := &cli.Command{
		Name:     "aicr",
		Commands: []*cli.Command{cmd},
	}

	err := app.Run(t.Context(), []string{"aicr", "node-validate"})
	if err == nil {
		t.Error("expected error when --recipe is not provided")
	}
}
