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
	"testing"

	"github.com/stretchr/testify/require"

	rcommon "helm.sh/helm/v4/pkg/release/common"
)

func TestRollback_ToLastDeployed(t *testing.T) {
	config := actionConfigFixture(t)
	mk := func(v int, st rcommon.Status) {
		rel := namedReleaseStub("angry-panda", st)
		rel.Version = v
		require.NoError(t, config.Releases.Create(rel))
	}
	mk(1, rcommon.StatusSuperseded)
	mk(2, rcommon.StatusDeployed)
	mk(3, rcommon.StatusFailed)

	rb := NewRollback(config)
	rb.ToLastDeployed = true
	_, target, _, err := rb.prepareRollback("angry-panda")
	require.NoError(t, err)
	require.Equal(t, 2, target.Info.RollbackRevision) // last DEPLOYED revision was v2
}

func TestRollback_RecoverPending(t *testing.T) {
	config := actionConfigFixture(t)
	deployed := namedReleaseStub("angry-panda", rcommon.StatusDeployed)
	deployed.Version = 1
	require.NoError(t, config.Releases.Create(deployed))
	stuck := namedReleaseStub("angry-panda", rcommon.StatusPendingUpgrade)
	stuck.Version = 2
	require.NoError(t, config.Releases.Create(stuck))

	rb := NewRollback(config)
	rb.RecoverPending = true
	rb.ToLastDeployed = true
	_, _, _, err := rb.prepareRollback("angry-panda")
	require.NoError(t, err)

	got, err := config.Releases.Get("angry-panda", 2)
	require.NoError(t, err)
	v2, err := releaserToV1Release(got)
	require.NoError(t, err)
	require.Equal(t, rcommon.StatusFailed, v2.Info.Status) // stuck pending release cleared to failed
}
