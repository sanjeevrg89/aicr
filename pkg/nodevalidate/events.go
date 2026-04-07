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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NVIDIA/aicr/pkg/errors"
	k8sclient "github.com/NVIDIA/aicr/pkg/k8s/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Event rate limiting: at most one event per (node, reason) per minInterval.
// Prevents event-spam when a node flaps or when a validation loop runs more
// often than the recipe state changes. Chosen to be complementary to metrics
// and labels, not a primary signal.
const eventMinInterval = 5 * time.Minute

// eventLimiter tracks the last emission time for each (node, reason) pair so
// duplicate events within eventMinInterval are dropped.
type eventLimiter struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func newEventLimiter() *eventLimiter {
	return &eventLimiter{lastSeen: make(map[string]time.Time)}
}

// allow returns true if an event for (node, reason) should be emitted now,
// and updates the last-seen time. Returns false if the event was emitted
// recently and should be suppressed.
func (l *eventLimiter) allow(node, reason string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := node + "|" + reason
	now := time.Now()
	if last, ok := l.lastSeen[key]; ok && now.Sub(last) < eventMinInterval {
		return false
	}
	l.lastSeen[key] = now
	return true
}

// defaultEventLimiter is the process-wide limiter used by EmitFailureEvent.
// The loop runs in a single process per DaemonSet pod so a package-level
// limiter is sufficient.
var defaultEventLimiter = newEventLimiter()

// EventReason constants used for the Reason field of emitted Events. Kept as
// a closed set so alerting rules can pattern-match.
const (
	EventReasonNonCompliant  = "RecipeNonCompliant"
	EventReasonValidationErr = "RecipeValidationError"
)

// EmitFailureEvent creates a Kubernetes Event describing why a node is
// non-compliant. Rate-limited per (node, reason) so a flapping node cannot
// flood the event stream.
//
// Events are complementary to the aicr_node_compliant metric and the
// aicr.nvidia.com/recipe-compliant label. They exist so operators can find
// per-constraint detail on the node without scraping per-constraint metrics.
//
// Emitting an Event requires RBAC permission to create events in the node's
// namespace (or the DaemonSet's namespace, since node events are namespaced).
// Callers should tolerate permission errors and log-and-continue.
func EmitFailureEvent(
	ctx context.Context,
	clientset k8sclient.Interface,
	namespace string,
	result *NodeResult,
) error {
	if result == nil || result.Compliant {
		return nil
	}
	if result.NodeName == "" {
		return errors.New(errors.ErrCodeInvalidRequest, "node name is empty — cannot emit event")
	}

	reason := EventReasonNonCompliant
	if !defaultEventLimiter.allow(result.NodeName, reason) {
		slog.Debug("event suppressed by rate limiter",
			slog.String("node", result.NodeName),
			slog.String("reason", reason))
		return nil
	}

	message := buildFailureMessage(result)

	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "aicr-node-validate-",
			Namespace:    namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:       "Node",
			Name:       result.NodeName,
			APIVersion: "v1",
		},
		Reason:  reason,
		Message: message,
		Type:    corev1.EventTypeWarning,
		Source: corev1.EventSource{
			Component: "aicr-node-validate",
			Host:      result.NodeName,
		},
		FirstTimestamp: metav1.Now(),
		LastTimestamp:  metav1.Now(),
		Count:          1,
	}

	if _, err := clientset.CoreV1().Events(namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil {
		return errors.Wrap(errors.ErrCodeInternal, "failed to create event", err)
	}
	slog.Info("emitted non-compliance event",
		slog.String("node", result.NodeName),
		slog.String("reason", reason))
	return nil
}

// buildFailureMessage summarizes the failing constraints in a single string
// suitable for an Event message. Truncated to keep events small.
func buildFailureMessage(result *NodeResult) string {
	failed := result.FailedConstraints()
	if len(failed) == 0 {
		return "Node is non-compliant with recipe"
	}

	const maxNames = 5
	names := make([]string, 0, len(failed))
	for i, cr := range failed {
		if i >= maxNames {
			names = append(names, fmt.Sprintf("(+%d more)", len(failed)-maxNames))
			break
		}
		names = append(names, cr.Name)
	}

	msg := fmt.Sprintf("Node %s failed %d recipe constraint(s):", result.NodeName, len(failed))
	for _, n := range names {
		msg += " " + n
	}
	return msg
}
