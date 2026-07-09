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
	"testing"

	"github.com/stretchr/testify/require"

	rcommon "helm.sh/helm/v4/pkg/release/common"
)

func TestUpgrade_RecoverPending(t *testing.T) {
	config := actionConfigFixture(t)
	deployed := namedReleaseStub("angry-panda", rcommon.StatusDeployed)
	deployed.Version = 1
	require.NoError(t, config.Releases.Create(deployed))
	stuck := namedReleaseStub("angry-panda", rcommon.StatusPendingUpgrade)
	stuck.Version = 2
	require.NoError(t, config.Releases.Create(stuck))

	up := NewUpgrade(config)
	up.RecoverPending = true
	_, _, _, err := up.prepareUpgrade(context.Background(), "angry-panda", buildChart(), map[string]any{})
	require.NotErrorIs(t, err, errPending) // recover-pending bypasses the pending lock

	// the stuck pending revision is now marked failed
	got, err := config.Releases.Get("angry-panda", 2)
	require.NoError(t, err)
	v2, err := releaserToV1Release(got)
	require.NoError(t, err)
	require.Equal(t, rcommon.StatusFailed, v2.Info.Status)
}
