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
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestCollectFailureDiagnostics(t *testing.T) {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "badapp-1", Namespace: "ns", Labels: map[string]string{"app": "badapp"}},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "app"}}},
	}
	warn := &v1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "ns"},
		Type:           v1.EventTypeWarning,
		Reason:         "Unhealthy",
		Message:        "Readiness probe failed",
		InvolvedObject: v1.ObjectReference{Name: "badapp-1", Namespace: "ns"},
		LastTimestamp:  metav1.Now(),
	}
	normal := &v1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "ns"},
		Type:           v1.EventTypeNormal,
		Reason:         "Scheduled",
		Message:        "Successfully assigned",
		InvolvedObject: v1.ObjectReference{Name: "badapp-1", Namespace: "ns"},
		LastTimestamp:  metav1.Now(),
	}
	c := &Client{kubeClient: fake.NewClientset(pod, warn, normal)}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "badapp", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "badapp"}},
		},
	}
	resources := ResourceList{{Name: "badapp", Namespace: "ns", Object: dep}}

	var buf bytes.Buffer
	c.CollectFailureDiagnostics(context.Background(), resources, "ns", &buf, DefaultDiagnosticsOptions())
	got := buf.String()
	require.Contains(t, got, "==> pod/badapp-1")                                  // pod found via the Deployment's selector
	require.Contains(t, got, "[event] Warning Unhealthy: Readiness probe failed") // Warning event surfaced
	require.NotContains(t, got, "Successfully assigned")                          // Normal event filtered out
	require.Contains(t, got, "[log:app] fake logs")                               // fake clientset log body streamed
}

func TestCollectFailureDiagnostics_EventsForbiddenFailsOpen(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("nope"))
	})
	c := &Client{kubeClient: cs}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "a"}}},
	}
	resources := ResourceList{{Name: "p", Namespace: "ns", Object: pod}}

	var buf bytes.Buffer
	require.NotPanics(t, func() {
		c.CollectFailureDiagnostics(context.Background(), resources, "ns", &buf, DefaultDiagnosticsOptions())
	})
	require.Contains(t, buf.String(), "could not fetch events")
}

func TestCollectFailureDiagnostics_CountAndRestarts(t *testing.T) {
	now := metav1.Now()
	first := metav1.NewTime(now.Time.Add(-19 * time.Minute))
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "crash-1", Namespace: "ns"},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "app"}}},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Name: "app", RestartCount: 12}},
		},
	}
	warn := &v1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "ns"},
		Type:           v1.EventTypeWarning,
		Reason:         "BackOff",
		Message:        "Back-off restarting failed container",
		InvolvedObject: v1.ObjectReference{Name: "crash-1", Namespace: "ns"},
		Count:          47,
		FirstTimestamp: first,
		LastTimestamp:  now,
	}
	c := &Client{kubeClient: fake.NewClientset(pod, warn)}
	resources := ResourceList{{Name: "crash-1", Namespace: "ns", Object: pod}}

	var buf bytes.Buffer
	c.CollectFailureDiagnostics(context.Background(), resources, "ns", &buf, DefaultDiagnosticsOptions())
	got := buf.String()
	require.Contains(t, got, "restarts: 12")        // pod restart count surfaced
	require.Contains(t, got, "(x47 over")           // aggregated event count + span
	require.Contains(t, got, "Back-off restarting") // message preserved
}
