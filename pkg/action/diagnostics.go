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

package action

import (
	"context"
	"os"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/kube"
	release "helm.sh/helm/v4/pkg/release/v1"
)

// showFailureDiagnostics prints bounded pod logs and Warning events for a
// failed release's resources to stderr. It is best-effort: it type-asserts to
// the concrete kube client (dry-run/fake clients are skipped) and rebuilds the
// resource list from the stored manifest when the caller does not have one.
func (cfg *Configuration) showFailureDiagnostics(rel *release.Release, resources kube.ResourceList, timeout time.Duration) {
	client, ok := cfg.KubeClient.(*kube.Client)
	if !ok || rel == nil {
		return
	}
	if resources == nil && rel.Manifest != "" {
		built, err := cfg.KubeClient.Build(strings.NewReader(rel.Manifest), false)
		if err != nil {
			return
		}
		resources = built
	}
	if len(resources) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts := kube.DefaultDiagnosticsOptions()
	if rel.Info != nil {
		opts.Since = rel.Info.LastDeployed
	}
	client.CollectFailureDiagnostics(ctx, resources, rel.Namespace, os.Stderr, opts)
}
