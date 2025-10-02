package server

import (
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/stretchr/testify/require"
)

func TestEIP712(t *testing.T) {
	t.Run("prod", func(t *testing.T) {
		tx := &v1.ManageEntityLegacy{
			UserId:     713621629,
			EntityType: "User",
			EntityId:   713621629,
			Action:     "Update",
			Metadata:   `{"cid":"bagaaiera2c6skimqkebrc2rmb6i5sx4a7zbglckodqzve7ot6kjxxrschnlq","data":{"name":"Matcha Male","handle":"matchamale","bio":"matcha is lyfe","location":"Aurora, CO","events":{},"is_deactivated":false,"allow_ai_attribution":false,"spl_usdc_payout_wallet":null}}`,
			Signature:  "0xd654719ec699cd00e06afb73bf7e1155c64b736bab9905f098ef11927c04afe82a90e72e8202592d6ad5ba6c2e1eac3120e34f4caea2a36d63f28434efa655551c",
			Nonce:      "0x8172f237c0ff351c2478fbb8ac5a21c4aa179304f8550e782284828191adf142",
		}

		config := &config.Config{
			AcdcChainID:              config.ProdAcdcChainID,
			AcdcEntityManagerAddress: config.ProdAcdcAddress,
		}

		address, _, err := RecoverPubkeyFromCoreTx(config, tx)
		require.Nil(t, err)
		t.Logf("recovered address: %s", address)
		require.Equal(t, "0x570d5bd4d4dbcc3f896ba095f4002a2545e5e5f6", strings.ToLower(address))
	})

	t.Run("stage", func(t *testing.T) {
		tx := &v1.ManageEntityLegacy{
			UserId:     800041733,
			EntityType: "User",
			EntityId:   800041733,
			Action:     "Update",
			Metadata:   `{"cid":"bagaaiera5eo2dqk6iuumfwi7gwvhgg57pn5qyg75njezxgieacnkf7qsohqq","data":{"name":"yayayayayay","handle":"yayayayayay","bio":"howdy pardner","location":"Denver, CO","events":{},"is_deactivated":false,"allow_ai_attribution":false}}`,
			Signature:  "0x217729f7823deb28aeeaa9451e25e0e21cffb073bc455b900382d9d5c25106b858c099248c12d40beaada4d7c0580dd7e3886ea9d06d2f6dc06ed66ceb2455e11b",
			Nonce:      "0x29a8bf36312f3aa4cf2257ceae6d2bb893f27d41372b763223ca9e7de93777d8",
		}

		config := &config.Config{
			AcdcChainID:              config.StageAcdcChainID,
			AcdcEntityManagerAddress: config.StageAcdcAddress,
		}

		address, _, err := RecoverPubkeyFromCoreTx(config, tx)
		require.Nil(t, err)
		t.Logf("recovered address: %s", address)
		require.Equal(t, "0x494388e8be6eb2af88ef0d999a07d9665bd00379", strings.ToLower(address))
	})
}
