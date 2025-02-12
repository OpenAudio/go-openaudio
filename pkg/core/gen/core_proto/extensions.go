package core_proto

import (
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/types"
	"google.golang.org/protobuf/proto"
)

func (stx *SignedTransaction) TxHash() string {
	b, err := proto.Marshal(stx)
	if err != nil {
		return ""
	}

	tx := types.Tx(b)
	hash := tx.Hash()
	hexBytes := bytes.HexBytes(hash)
	hashStr := hexBytes.String()

	return hashStr
}
