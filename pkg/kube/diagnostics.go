/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DiagnosticsOptions bounds how much diagnostic data is collected on failure.
type DiagnosticsOptions struct {
	// TailLines is the maximum number of log lines fetched per container.
	TailLines int64
	// MaxPods caps how many pods are inspected across all resources.
	MaxPods int
	// Since limits events to those at or after this time (the operation start).
	// The zero value disables time filtering.
	Since time.Time
}

// DefaultDiagnosticsOptions returns conservative, CI-friendly bounds.
func DefaultDiagnosticsOptions() DiagnosticsOptions {
	return DiagnosticsOptions{TailLines: 25, MaxPods: 5}
}

// CollectFailureDiagnostics writes bounded pod logs and Warning events for the
// given resources to out. It is best-effort and fail-open: any RBAC or API
// error is written inline and does not stop collection, and it never returns an
// error. This lets callers invoke it from a failure path without introducing a
// new failure mode.
//
// Output is line-oriented and self-labeling so events and logs can be told
// apart at a glance and filtered with grep:
//
//	==> failure diagnostics (--show-logs-on-failure): 3 pod(s) not ready
//	==> pod/web-abc
//	    [event] Warning Unhealthy: Readiness probe failed
//	    [log:app] starting app...
//	    [log:app] sh: myapp-server: not found
func (c *Client) CollectFailureDiagnostics(ctx context.Context, resources ResourceList, namespace string, out io.Writer, opts DiagnosticsOptions) {
	if opts.MaxPods <= 0 {
		opts.MaxPods = 5
	}
	if opts.TailLines <= 0 {
		opts.TailLines = 25
	}

	pods := c.podsForResources(resources, namespace, out)
	total := len(pods)
	if total == 0 {
		return
	}
	shown := pods
	if total > opts.MaxPods {
		shown = pods[:opts.MaxPods]
	}

	fmt.Fprintf(out, "==> failure diagnostics (--show-logs-on-failure): %d pod(s) not ready\n", total)
	for i := range shown {
		p := &shown[i]
		if r := podRestarts(p); r > 0 {
			fmt.Fprintf(out, "==> pod/%s (restarts: %d)\n", p.Name, r)
		} else {
			fmt.Fprintf(out, "==> pod/%s\n", p.Name)
		}
		c.writeWarningEvents(ctx, p.Name, p.Namespace, opts.Since, out)
		c.writePodLogs(ctx, p, opts.TailLines, out)
	}
	if total > len(shown) {
		fmt.Fprintf(out, "==> (showing first %d of %d pods; %d more omitted)\n", len(shown), total, total-len(shown))
	}
}

// podsForResources resolves the pods belonging to the given (not-ready)
// resources by each workload's selector; bare pods map to themselves.
func (c *Client) podsForResources(resources ResourceList, namespace string, out io.Writer) []v1.Pod {
	var pods []v1.Pod
	seen := map[string]bool{}

	addSelector := func(sel *metav1.LabelSelector) {
		s, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil {
			return
		}
		list, err := c.GetPodList(namespace, metav1.ListOptions{LabelSelector: s.String()})
		if err != nil {
			fmt.Fprintf(out, "    could not list pods: %v\n", err)
			return
		}
		for _, p := range list.Items {
			if !seen[p.Name] {
				seen[p.Name] = true
				pods = append(pods, p)
			}
		}
	}

	for _, info := range resources {
		switch obj := AsVersioned(info).(type) {
		case *appsv1.Deployment:
			addSelector(obj.Spec.Selector)
		case *appsv1.StatefulSet:
			addSelector(obj.Spec.Selector)
		case *appsv1.DaemonSet:
			addSelector(obj.Spec.Selector)
		case *appsv1.ReplicaSet:
			addSelector(obj.Spec.Selector)
		case *batchv1.Job:
			addSelector(obj.Spec.Selector)
		case *v1.Pod:
			if !seen[obj.Name] {
				seen[obj.Name] = true
				pods = append(pods, *obj)
			}
		}
	}
	return pods
}

// writePodLogs streams the last tail lines of every container in pod to out,
// tagging each line with [log:<container>] so it is unambiguous.
func (c *Client) writePodLogs(ctx context.Context, pod *v1.Pod, tail int64, out io.Writer) {
	for _, container := range pod.Spec.Containers {
		req := c.kubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &v1.PodLogOptions{
			Container: container.Name,
			TailLines: &tail,
		})
		stream, err := req.Stream(ctx)
		if err != nil {
			fmt.Fprintf(out, "    [log:%s] could not fetch logs: %v\n", container.Name, err)
			continue
		}
		sc := bufio.NewScanner(stream)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Fprintf(out, "    [log:%s] %s\n", container.Name, sc.Text())
		}
		_ = stream.Close()
	}
}

// writeWarningEvents lists Warning events involving the named object and writes
// those within the since window to out, oldest first, tagged with [event].
func (c *Client) writeWarningEvents(ctx context.Context, name, namespace string, since time.Time, out io.Writer) {
	list, err := c.kubeClient.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", name),
	})
	if err != nil {
		fmt.Fprintf(out, "    [event] could not fetch events for %s: %v\n", name, err)
		return
	}
	events := list.Items
	sort.Slice(events, func(i, j int) bool {
		return events[i].LastTimestamp.Before(&events[j].LastTimestamp)
	})
	for _, e := range events {
		// Filter client-side too: the fake clientset ignores field selectors,
		// and we only ever want Warnings within the window.
		if e.InvolvedObject.Name != name || e.Type != v1.EventTypeWarning {
			continue
		}
		if !since.IsZero() && e.LastTimestamp.Time.Before(since) {
			continue
		}
		count, span := eventCountAndSpan(e)
		switch {
		case count > 1 && span > 0:
			fmt.Fprintf(out, "    [event] Warning %s (x%d over %s): %s\n", e.Reason, count, span.Round(time.Second), e.Message)
		case count > 1:
			fmt.Fprintf(out, "    [event] Warning %s (x%d): %s\n", e.Reason, count, e.Message)
		default:
			fmt.Fprintf(out, "    [event] Warning %s: %s\n", e.Reason, e.Message)
		}
	}
}

// podRestarts sums the restart counts across a pod's containers.
func podRestarts(p *v1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

// eventCountAndSpan returns how many times an event occurred and the time span
// it covered, handling both the legacy count fields and the newer Series field.
func eventCountAndSpan(e v1.Event) (int32, time.Duration) {
	count := e.Count
	last := e.LastTimestamp.Time
	if e.Series != nil {
		if e.Series.Count > count {
			count = e.Series.Count
		}
		if !e.Series.LastObservedTime.IsZero() {
			last = e.Series.LastObservedTime.Time
		}
	}
	if count < 1 {
		count = 1
	}
	span := last.Sub(e.FirstTimestamp.Time)
	if span < 0 {
		span = 0
	}
	return count, span
}
