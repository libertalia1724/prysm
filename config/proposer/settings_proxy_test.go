package proposer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OffchainLabs/prysm/v7/config"
	"github.com/OffchainLabs/prysm/v7/config/proposer"
	validatorpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1/validator-client"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func TestBuilderConfig_ProxyFromFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(file, []byte(`{
		"default_config": {
			"fee_recipient": "0x8943545177806ED17B9F23F0a21ee5948eCaa776",
			"builder": {
				"enabled": true,
				"builders": [
					{"url": "http://builder-a:8080"},
					{"url": "http://builder-b:8080", "pubkey": "0xb0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7", "proxy": "http://other:9100", "max_execution_payment": "77"}
				],
				"proxy": "http://sidecar:9000",
				"max_execution_payment": "1000"
			}
		}
	}`), 0o600))

	var payload *validatorpb.ProposerSettingsPayload
	require.NoError(t, config.UnmarshalFromFile(file, &payload))
	settings, err := proposer.SettingFromConsensus(payload)
	require.NoError(t, err)

	bc := settings.DefaultConfig.BuilderConfig
	require.NotNil(t, bc)
	require.Equal(t, "http://sidecar:9000", bc.Proxy)
	require.Equal(t, 2, len(bc.Builders))
	require.Equal(t, uint64(1000), uint64(bc.MaxExecutionPayment))
	require.Equal(t, "http://builder-a:8080", bc.Builders[0].URL)
	require.Equal(t, "", bc.Builders[0].Pubkey)
	require.Equal(t, "http://other:9100", bc.Builders[1].Proxy)
	require.Equal(t, uint64(77), uint64(bc.Builders[1].MaxExecutionPayment))

	// Everything survives the clone and the consensus round-trip used by the DB.
	require.Equal(t, "http://sidecar:9000", bc.Clone().Proxy)
	roundTrip, err := proposer.SettingFromConsensus(settings.ToConsensus())
	require.NoError(t, err)
	rbc := roundTrip.DefaultConfig.BuilderConfig
	require.Equal(t, "http://sidecar:9000", rbc.Proxy)
	require.DeepEqual(t, bc.Builders, rbc.Builders)
}

func TestBuilderConfig_RelaysDeprecatedAlias(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(file, []byte(`{
		"default_config": {
			"fee_recipient": "0x8943545177806ED17B9F23F0a21ee5948eCaa776",
			"builder": {
				"enabled": true,
				"relays": ["http://builder-a:8080"]
			}
		}
	}`), 0o600))

	var payload *validatorpb.ProposerSettingsPayload
	require.NoError(t, config.UnmarshalFromFile(file, &payload))
	settings, err := proposer.SettingFromConsensus(payload)
	require.NoError(t, err)

	bc := settings.DefaultConfig.BuilderConfig
	require.NotNil(t, bc)
	require.Equal(t, 1, len(bc.Builders))
	require.Equal(t, "http://builder-a:8080", bc.Builders[0].URL)
}
