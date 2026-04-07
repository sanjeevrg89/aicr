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
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/aicr/pkg/diff"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEventLimiter_AllowsFirstEvent(t *testing.T) {
	l := newEventLimiter()
	if !l.allow("node-a", "RecipeNonCompliant") {
		t.Errorf("first event for a key should be allowed")
	}
}

func TestEventLimiter_SuppressesWithinInterval(t *testing.T) {
	l := newEventLimiter()
	l.allow("node-a", "RecipeNonCompliant")
	if l.allow("node-a", "RecipeNonCompliant") {
		t.Errorf("duplicate event within interval should be suppressed")
	}
}

func TestEventLimiter_AllowsDifferentKeys(t *testing.T) {
	l := newEventLimiter()
	l.allow("node-a", "RecipeNonCompliant")
	if !l.allow("node-b", "RecipeNonCompliant") {
		t.Errorf("different node should not be rate limited")
	}
	if !l.allow("node-a", "RecipeValidationError") {
		t.Errorf("different reason should not be rate limited")
	}
}

func TestEventLimiter_AllowsAfterInterval(t *testing.T) {
	l := newEventLimiter()
	// Pre-seed a stale entry well beyond the interval.
	l.lastSeen["node-a|RecipeNonCompliant"] = time.Now().Add(-2 * eventMinInterval)
	if !l.allow("node-a", "RecipeNonCompliant") {
		t.Errorf("event after interval should be allowed")
	}
}

func TestBuildFailureMessage_NoConstraints(t *testing.T) {
	r := &NodeResult{
		NodeName: "n1",
		DiffResult: &diff.Result{
			ConstraintResults: []diff.ConstraintResult{},
		},
	}
	msg := buildFailureMessage(r)
	if !strings.Contains(msg, "non-compliant") {
		t.Errorf("expected generic non-compliant message, got %q", msg)
	}
}

func TestBuildFailureMessage_TruncatesAtMax(t *testing.T) {
	results := make([]diff.ConstraintResult, 10)
	for i := range results {
		results[i] = diff.ConstraintResult{
			Name:   "constraint-" + string(rune('0'+i)),
			Passed: false,
		}
	}
	r := &NodeResult{
		NodeName:   "n1",
		DiffResult: &diff.Result{ConstraintResults: results},
	}
	msg := buildFailureMessage(r)
	if !strings.Contains(msg, "+5 more") {
		t.Errorf("expected truncation marker '+5 more' in message, got %q", msg)
	}
}

func TestEmitFailureEvent_SkipsWhenCompliant(t *testing.T) {
	client := fake.NewSimpleClientset()
	r := &NodeResult{NodeName: "n1", Compliant: true}
	if err := EmitFailureEvent(context.Background(), client, "default", r); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// No event should have been created.
	evts, _ := client.CoreV1().Events("default").List(context.Background(), metav1.ListOptions{})
	if len(evts.Items) != 0 {
		t.Errorf("expected 0 events for compliant node, got %d", len(evts.Items))
	}
}

func TestEmitFailureEvent_CreatesWarning(t *testing.T) {
	// Fresh limiter to avoid interference from other tests in the package.
	defaultEventLimiter = newEventLimiter()

	client := fake.NewSimpleClientset()
	r := &NodeResult{
		NodeName:  "n1",
		Compliant: false,
		DiffResult: &diff.Result{
			ConstraintResults: []diff.ConstraintResult{
				{Name: "GPU.device.driver", Passed: false},
			},
		},
	}
	if err := EmitFailureEvent(context.Background(), client, "aicr-system", r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evts, err := client.CoreV1().Events("aicr-system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts.Items) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts.Items))
	}
	evt := evts.Items[0]
	if evt.Type != corev1.EventTypeWarning {
		t.Errorf("expected Warning event, got %q", evt.Type)
	}
	if evt.Reason != EventReasonNonCompliant {
		t.Errorf("expected reason %q, got %q", EventReasonNonCompliant, evt.Reason)
	}
	if evt.InvolvedObject.Kind != "Node" || evt.InvolvedObject.Name != "n1" {
		t.Errorf("unexpected involved object: %+v", evt.InvolvedObject)
	}
}

func TestEmitFailureEvent_RateLimited(t *testing.T) {
	defaultEventLimiter = newEventLimiter()

	client := fake.NewSimpleClientset()
	r := &NodeResult{
		NodeName:  "n1",
		Compliant: false,
		DiffResult: &diff.Result{
			ConstraintResults: []diff.ConstraintResult{{Name: "x", Passed: false}},
		},
	}
	if err := EmitFailureEvent(context.Background(), client, "ns", r); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	if err := EmitFailureEvent(context.Background(), client, "ns", r); err != nil {
		t.Fatalf("second emit (should be suppressed, not error): %v", err)
	}
	evts, _ := client.CoreV1().Events("ns").List(context.Background(), metav1.ListOptions{})
	if len(evts.Items) != 1 {
		t.Errorf("expected 1 event after rate-limited duplicate, got %d", len(evts.Items))
	}
}
