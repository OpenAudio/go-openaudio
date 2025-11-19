package server

import (
	"context"
	"fmt"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reads block from db and returns it in v1.Block format
func (s *Server) GetBlock(ctx context.Context, height int64, canon bool) (*v1.Block, error) {
	rows, err := s.db.GetBlockWithTransactions(ctx, height)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("block not found")
	}

	dbBlock := rows[0]
	block := &v1.Block{
		Hash:         dbBlock.BlockHash,
		ChainId:      dbBlock.ChainID,
		Proposer:     dbBlock.Proposer,
		Height:       dbBlock.Height,
		Transactions: make([]*v1.Transaction, 0, len(rows)-1),
		Timestamp:    timestamppb.New(dbBlock.BlockCreatedAt.Time),
	}

	if len(rows) == 1 {
		return block, nil
	}

	for _, dbTx := range rows[1:] {
		tx := &v1.Transaction{
			Hash:          dbTx.TxHash.String,
			BlockHash:     dbBlock.BlockHash,
			ChainId:       dbBlock.ChainID,
			Height:        dbBlock.Height,
			Timestamp:     timestamppb.New(dbTx.TxCreatedAt.Time),
			Transaction:   &v1.SignedTransaction{},
			Transactionv2: &v1beta1.Transaction{},
		}

		var isV1, isV2 bool

		if err := proto.Unmarshal(dbTx.Transaction, tx.Transaction); err == nil {
			isV1 = true
		}

		if !isV1 {
			if err := proto.Unmarshal(dbTx.Transaction, tx.Transactionv2); err == nil {
				isV2 = true
			}
		}

		if !isV1 && !isV2 {
			return nil, fmt.Errorf("invalid transaction: %s", dbTx.TxHash.String)
		}

		block.Transactions = append(block.Transactions, tx)
	}

	if !canon {
		block.Transactions = sortTransactionResponse(block.Transactions)
	}

	return block, nil
}
