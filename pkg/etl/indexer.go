package etl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/OpenAudio/go-openaudio/etl/processors"
	em "github.com/OpenAudio/go-openaudio/etl/processors/entity_manager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// ChallengeStats represents storage proof challenge statistics for a validator
type ChallengeStats struct {
	ChallengesReceived int32
	ChallengesFailed   int32
}

// StorageProofState tracks storage proof challenges and their resolution
type StorageProofState struct {
	Height          int64
	Proofs          map[string]*StorageProofEntry // address -> proof entry
	ProverAddresses map[string]int                // address -> vote count for who should be provers
	Resolved        bool
}

type StorageProofEntry struct {
	Address         string
	ProverAddresses []string
	ProofSignature  []byte
	Cid             string
	SignatureValid  bool // determined during verification
}

func (e *Indexer) Run() error {
	dbUrl := e.dbURL
	if dbUrl == "" {
		return fmt.Errorf("dbUrl environment variable not set")
	}

	err := db.RunMigrations(e.logger, dbUrl, e.runDownMigrations)
	if err != nil {
		return fmt.Errorf("error running migrations: %v", err)
	}

	pgConfig, err := pgxpool.ParseConfig(dbUrl)
	if err != nil {
		return fmt.Errorf("error parsing database config: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), pgConfig)
	if err != nil {
		return fmt.Errorf("error creating database pool: %v", err)
	}

	e.pool = pool
	e.db = db.New(pool)

	// Initialize entity manager dispatcher and register handlers
	e.dispatcher = em.NewDispatcher(e.logger)
	if e.config.IsDataTypeEnabled(em.EntityTypeUser) {
		e.dispatcher.Register(em.UserCreate())
	}

	if e.dispatcher.HandlerCount() > 0 {
		e.logger.Info("entity manager enabled", zap.Int("handlers", e.dispatcher.HandlerCount()))
	} else {
		e.logger.Info("entity manager disabled (no data types enabled)")
	}

	// Initialize pubsub instances
	e.blockPubsub = NewPubsub[*db.EtlBlock]()
	e.playPubsub = NewPubsub[*db.EtlPlay]()

	// Initialize materialized view refresher
	e.mvRefresher = NewMaterializedViewRefresher(e.pool, e.logger)

	// Initialize chain ID from core service
	err = e.InitializeChainID(context.Background())
	if err != nil {
		e.logger.Error("error initializing chain ID", zap.Error(err))
	}

	e.logger.Info("starting etl service")

	if e.checkReadiness {
		err = e.awaitReadiness()
		if err != nil {
			e.logger.Error("error awaiting readiness", zap.Error(err))
		}
	}

	ctx := context.Background()
	g, gCtx := errgroup.WithContext(ctx)

	if e.config.EnableMaterializedViewRefresh {
		g.Go(func() error {
			return e.mvRefresher.Start(gCtx)
		})
	}

	if e.config.EnablePgNotifyListener {
		g.Go(func() error {
			return e.startPgNotifyListener(gCtx)
		})
	}

	g.Go(func() error {
		if err := e.indexBlocks(); err != nil {
			return fmt.Errorf("error indexing blocks: %v", err)
		}

		return nil
	})

	g.Go(func() error {
		return e.syncValidatorsFromCore(gCtx)
	})

	return g.Wait()
}

func (e *Indexer) awaitReadiness() error {
	e.logger.Info("awaiting readiness")
	attempts := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		attempts++
		if attempts > 60 {
			return fmt.Errorf("timed out waiting for readiness")
		}

		res, err := e.core.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
		if err != nil {
			continue
		}

		if res.Msg.Ready {
			return nil
		}
	}

	return nil
}

func (e *Indexer) indexBlocks() error {
	for {
		// Get the latest indexed block height
		latestHeight, err := e.db.GetLatestIndexedBlock(context.Background())
		if err != nil {
			// If no records exist, start from block 1
			if errors.Is(err, pgx.ErrNoRows) {
				if e.startingBlockHeight > 0 {
					// Start from block 1 (nextHeight will be 1)
					latestHeight = e.startingBlockHeight - 1
				} else {
					// Start from block 1 (nextHeight will be 1)
					latestHeight = 0
				}
			} else {
				e.logger.Error("error getting latest indexed block", zap.Error(err))
				continue
			}
		}

		// Get the next block
		nextHeight := latestHeight + 1
		block, err := e.core.GetBlock(context.Background(), connect.NewRequest(&corev1.GetBlockRequest{
			Height: nextHeight,
		}))
		if err != nil {
			e.logger.Error("error getting block", zap.Int64("height", nextHeight), zap.Error(err))
			continue
		}

		if block.Msg.Block.Height < 0 {
			continue
		}

		// Insert block first
		err = e.db.InsertBlock(context.Background(), db.InsertBlockParams{
			ProposerAddress: block.Msg.Block.Proposer,
			BlockHeight:     block.Msg.Block.Height,
			BlockTime:       pgtype.Timestamp{Time: block.Msg.Block.Timestamp.AsTime(), Valid: true},
		})
		if err != nil {
			e.logger.Error("error inserting block", zap.Int64("height", nextHeight), zap.Error(err))
			continue
		}

		var wg sync.WaitGroup
		wg.Add(len(block.Msg.Block.Transactions))

		for index := range block.Msg.Block.Transactions {
			go func(block *corev1.Block, index int) {
				defer wg.Done()

				tx := block.Transactions[index]
				insertTxParams := db.InsertTransactionParams{
					TxHash:      tx.Hash,
					BlockHeight: block.Height,
					TxIndex:     int32(index),
					TxType:      "",                        // We'll update this after determining the type
					Address:     pgtype.Text{Valid: false}, // We'll update this after determining the address
					CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
				}

				switch signedTx := tx.Transaction.Transaction.(type) {
			case *corev1.SignedTransaction_Plays:
				e.logger.Debug("processing tx",
					zap.String("type", "play"),
					zap.String("hash", tx.Hash),
					zap.Int("play_count", len(signedTx.Plays.GetPlays())),
				)
				txCtx := &processors.TxContext{
						Block:     block,
						TxHash:    tx.Hash,
						TxIndex:   index,
						BlockTime: pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
						InsertTx:  insertTxParams,
					}
					res, err := processors.Play().Process(context.Background(), tx.Transaction, txCtx, e.db)
					if err != nil {
						e.logger.Error("error processing plays", zap.Error(err))
					} else {
						insertTxParams = res.InsertTx
					}

			case *corev1.SignedTransaction_ManageEntity:
				me := signedTx.ManageEntity
				e.logger.Debug("processing tx",
					zap.String("type", "manage_entity"),
					zap.String("hash", tx.Hash),
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("signer", me.GetSigner()),
				)

				txCtx := &processors.TxContext{
					Block:     block,
					TxHash:    tx.Hash,
					TxIndex:   index,
					BlockTime: pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					InsertTx:  insertTxParams,
				}
				res, err := processors.ManageEntity().Process(context.Background(), tx.Transaction, txCtx, e.db)
				if err != nil {
					e.logger.Error("error processing manage entity", zap.Error(err))
				} else {
					insertTxParams = res.InsertTx
				}

				txStart := time.Now()
				emParams := em.NewParams(me, block.Height, block.Timestamp.AsTime(), tx.Hash, e.pool, e.logger)
				if dErr := e.dispatcher.Dispatch(context.Background(), emParams); dErr != nil {
					if em.IsValidationError(dErr) {
						e.logger.Debug("entity manager validation failed",
							zap.String("hash", tx.Hash),
							zap.String("entity_type", me.GetEntityType()),
							zap.String("action", me.GetAction()),
							zap.String("reason", dErr.Error()),
						)
					} else {
						e.logger.Error("entity manager dispatch error", zap.Error(dErr))
					}
				} else {
					e.logger.Debug("tx indexed",
						zap.String("type", "manage_entity"),
						zap.String("hash", tx.Hash),
						zap.Duration("elapsed", time.Since(txStart)),
					)
				}

				case *corev1.SignedTransaction_ValidatorRegistration:
					insertTxParams.TxType = TxTypeValidatorRegistrationLegacy
					// Legacy validator registration - no specific table insert needed
				case *corev1.SignedTransaction_ValidatorDeregistration:
					insertTxParams.TxType = TxTypeValidatorMisbehaviorDereg
					vd := signedTx.ValidatorDeregistration
					// For deregistration we only have comet address, we'll need to look up eth address
					// For now use comet address, can be improved later
					insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
					err = e.db.InsertValidatorMisbehaviorDeregistration(context.Background(), db.InsertValidatorMisbehaviorDeregistrationParams{
						CometAddress: vd.CometAddress,
						PubKey:       vd.PubKey,
						BlockHeight:  block.Height,
						TxHash:       tx.Hash,
						CreatedAt:    pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting validator misbehavior deregistration", zap.Error(err))
					}
				case *corev1.SignedTransaction_SlaRollup:
					insertTxParams.TxType = TxTypeSlaRollup
					sr := signedTx.SlaRollup
					// SLA rollups affect multiple validators, so we leave address as null

					// Use the number of reports in the rollup as the validator count
					// This matches what the original core system does
					validatorCount := int32(len(sr.Reports))

					// Calculate block quota (total blocks divided by number of validators)
					var blockQuota int32 = 0
					if sr.BlockEnd > sr.BlockStart && validatorCount > 0 {
						blockQuota = int32(sr.BlockEnd-sr.BlockStart) / validatorCount
					}

					// Calculate BPS and TPS for this rollup period
					blockRange := sr.BlockEnd - sr.BlockStart
					var bps, tps float64 = 0.0, 0.0

					if blockRange > 0 {
						// Get transaction count for this block range
						txCount := int64(0)
						for blockHeight := sr.BlockStart; blockHeight <= sr.BlockEnd; blockHeight++ {
							blockTxCount, err := e.db.GetBlockTransactionCount(context.Background(), blockHeight)
							if err != nil {
								e.logger.Debug("failed to get transaction count for block", zap.Int64("height", blockHeight), zap.Error(err))
								continue
							}
							txCount += blockTxCount
						}

						// Calculate time duration from the rollup timestamp and previous rollup
						rollupTime := sr.Timestamp.AsTime()
						var duration float64 = 0

						// Try to get the previous rollup to calculate time difference
						if latestRollup, err := e.db.GetLatestSlaRollup(context.Background()); err == nil {
							if latestRollup.CreatedAt.Valid {
								duration = rollupTime.Sub(latestRollup.CreatedAt.Time).Seconds()
							}
						}

						// If we couldn't get duration from previous rollup, estimate from block count
						// Assuming average block time of 2 seconds
						if duration <= 0 {
							duration = float64(blockRange) * 2.0
						}

						// Calculate BPS and TPS
						if duration > 0 {
							bps = float64(blockRange) / duration
							tps = float64(txCount) / duration
						}
					}

					// Insert SLA rollup and get the ID
					rollupId, err := e.db.InsertSlaRollupReturningId(context.Background(), db.InsertSlaRollupReturningIdParams{
						BlockStart:     sr.BlockStart,
						BlockEnd:       sr.BlockEnd,
						BlockHeight:    block.Height,
						ValidatorCount: validatorCount,
						BlockQuota:     blockQuota,
						Bps:            bps,
						Tps:            tps,
						TxHash:         tx.Hash,
						CreatedAt:      pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true}, // Use rollup timestamp, not block timestamp
					})
					if err != nil {
						e.logger.Error("error inserting SLA rollup", zap.Error(err))
					} else {
						// Get storage proof challenge statistics for this SLA period
						challengeStats, err := e.calculateChallengeStatistics(sr.BlockStart, sr.BlockEnd)
						if err != nil {
							e.logger.Error("error calculating challenge statistics", zap.Error(err))
							challengeStats = make(map[string]ChallengeStats) // fallback to empty map
						}

						// Insert SLA node reports with the actual rollup ID and challenge data
						for _, report := range sr.Reports {
							stats := challengeStats[report.Address] // Get challenge stats for this validator

							err = e.db.InsertSlaNodeReport(context.Background(), db.InsertSlaNodeReportParams{
								SlaRollupID:        rollupId, // Use the actual rollup ID
								Address:            report.Address,
								NumBlocksProposed:  report.NumBlocksProposed,
								ChallengesReceived: stats.ChallengesReceived,
								ChallengesFailed:   stats.ChallengesFailed,
								BlockHeight:        block.Height,
								TxHash:             tx.Hash,
								CreatedAt:          pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true}, // Use rollup timestamp
							})
							if err != nil {
								e.logger.Error("error inserting SLA node report", zap.Error(err))
							}
						}
					}
				case *corev1.SignedTransaction_StorageProof:
					insertTxParams.TxType = TxTypeStorageProof
					sp := signedTx.StorageProof
					insertTxParams.Address = pgtype.Text{String: sp.Address, Valid: true}
					err = e.db.InsertStorageProof(context.Background(), db.InsertStorageProofParams{
						Height:          sp.Height,
						Address:         sp.Address,
						ProverAddresses: sp.ProverAddresses,
						Cid:             sp.Cid,
						ProofSignature:  sp.ProofSignature,
						Proof:           nil, // Will be set during verification
						Status:          "unresolved",
						BlockHeight:     block.Height,
						TxHash:          tx.Hash,
						CreatedAt:       pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting storage proof", zap.Error(err))
					}
				case *corev1.SignedTransaction_StorageProofVerification:
					insertTxParams.TxType = TxTypeStorageProofVerification
					spv := signedTx.StorageProofVerification
					// Storage proof verification doesn't have a specific address, leave as null
					err = e.db.InsertStorageProofVerification(context.Background(), db.InsertStorageProofVerificationParams{
						Height:      spv.Height,
						Proof:       spv.Proof,
						BlockHeight: block.Height,
						TxHash:      tx.Hash,
						CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting storage proof verification", zap.Error(err))
					} else {
						// Process consensus for this storage proof challenge
						err = e.processStorageProofConsensus(spv.Height, spv.Proof, block.Height, tx.Hash, block.Timestamp.AsTime())
						if err != nil {
							e.logger.Error("error processing storage proof consensus", zap.Error(err))
						}
					}
				case *corev1.SignedTransaction_Attestation:
					at := signedTx.Attestation
					if vr := at.GetValidatorRegistration(); vr != nil {
						insertTxParams.TxType = TxTypeValidatorRegistration
						insertTxParams.Address = pgtype.Text{String: vr.DelegateWallet, Valid: true}
						err = e.db.InsertValidatorRegistration(context.Background(), db.InsertValidatorRegistrationParams{
							Address:      vr.DelegateWallet,
							Endpoint:     vr.Endpoint,
							CometAddress: vr.CometAddress,
							EthBlock:     fmt.Sprintf("%d", vr.EthBlock),
							NodeType:     vr.NodeType,
							Spid:         vr.SpId,
							CometPubkey:  vr.PubKey,
							VotingPower:  vr.Power,
							BlockHeight:  block.Height,
							TxHash:       tx.Hash,
						})
						if err != nil {
							e.logger.Error("error inserting validator registration", zap.Error(err))
						}
						// insert RegisteredValidator record
						err = e.db.RegisterValidator(context.Background(), db.RegisterValidatorParams{
							Address:        vr.DelegateWallet,
							Endpoint:       vr.Endpoint,
							CometAddress:   vr.CometAddress,
							NodeType:       vr.NodeType,
							Spid:           vr.SpId,
							VotingPower:    vr.Power,
							Status:         "active",
							RegisteredAt:   block.Height,
							DeregisteredAt: pgtype.Int8{Valid: false},
							CreatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
							UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
						})
						if err != nil {
							e.logger.Error("error registering validator", zap.Error(err))
						}
					}
					if vd := at.GetValidatorDeregistration(); vd != nil {
						insertTxParams.TxType = TxTypeValidatorDeregistration
						// For attestation deregistration we only have comet address, need to look up eth address
						// For now use comet address, can be improved later
						insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
						err = e.db.InsertValidatorDeregistration(context.Background(), db.InsertValidatorDeregistrationParams{
							CometAddress: vd.CometAddress,
							CometPubkey:  vd.PubKey,
							BlockHeight:  block.Height,
							TxHash:       tx.Hash,
						})
						if err != nil {
							e.logger.Error("error inserting validator deregistration", zap.Error(err))
						}
						// insert DeregisteredValidator record
						err = e.db.DeregisterValidator(context.Background(), db.DeregisterValidatorParams{
							DeregisteredAt: pgtype.Int8{Int64: block.Height, Valid: true},
							UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
							Status:         "deregistered",
							CometAddress:   vd.CometAddress,
						})
						if err != nil {
							e.logger.Error("error deregistering validator", zap.Error(err))
						}
					}
				}

				err = e.db.InsertTransaction(context.Background(), insertTxParams)
				if err != nil {
					e.logger.Error("error inserting transaction", zap.String("tx", tx.Hash), zap.Error(err))
					return
				}

			}(block.Msg.Block, index)
		}

		wg.Wait()

		// TODO: use pgnotify to publish block and play events to pubsub

		if e.endingBlockHeight > 0 && block.Msg.Block.Height >= e.endingBlockHeight {
			e.logger.Info("ending block height reached, stopping etl service")
			return nil
		}
	}
}

func (e *Indexer) startPgNotifyListener(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, e.dbURL)
	if err != nil {
		return fmt.Errorf("failed to connect for notifications: %w", err)
	}
	defer conn.Close(ctx)

	// Listen to both channels
	_, err = conn.Exec(ctx, "LISTEN new_block")
	if err != nil {
		return fmt.Errorf("failed to listen to new_block: %w", err)
	}

	_, err = conn.Exec(ctx, "LISTEN new_plays")
	if err != nil {
		return fmt.Errorf("failed to listen to new_plays: %w", err)
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("error waiting for notification: %w", err)
		}

		switch notification.Channel {
		case "new_block":
			block := &db.EtlBlock{}
			err = json.Unmarshal([]byte(notification.Payload), block)
			if err != nil {
				e.logger.Error("error unmarshalling block", zap.Error(err))
				continue
			}
			if e.blockPubsub.HasSubscribers(BlockTopic) {
				e.blockPubsub.Publish(context.Background(), BlockTopic, block)
			}
		case "new_plays":
			play := &db.EtlPlay{}
			err = json.Unmarshal([]byte(notification.Payload), play)
			if err != nil {
				e.logger.Error("error unmarshalling play", zap.Error(err))
				continue
			}
			if e.playPubsub.HasSubscribers(PlayTopic) {
				e.playPubsub.Publish(context.Background(), PlayTopic, play)
			}
		}
	}
}

// calculateChallengeStatistics aggregates storage proof challenge data for validators within a block range
// NOTE: This function may be called before all storage proof data for the block range is available,
// leading to potentially inaccurate pre-calculated statistics. Consider calculating these dynamically
// in the UI instead of storing them in the database.
func (e *Indexer) calculateChallengeStatistics(blockStart, blockEnd int64) (map[string]ChallengeStats, error) {
	ctx := context.Background()
	stats := make(map[string]ChallengeStats)

	// Use the ETL database method to get challenge statistics with proper status tracking
	results, err := e.db.GetChallengeStatisticsForBlockRange(ctx, db.GetChallengeStatisticsForBlockRangeParams{
		Height:   blockStart,
		Height_2: blockEnd,
	})
	if err != nil {
		return stats, fmt.Errorf("error querying challenge statistics: %v", err)
	}

	// Convert results to our ChallengeStats map
	for _, result := range results {
		stats[result.Address] = ChallengeStats{
			ChallengesReceived: int32(result.ChallengesReceived),
			ChallengesFailed:   int32(result.ChallengesFailed),
		}
	}

	return stats, nil
}

func (e *Indexer) processStorageProofConsensus(height int64, proof []byte, blockHeight int64, txHash string, blockTime time.Time) error {
	ctx := context.Background()

	// Get all storage proofs for this height
	storageProofs, err := e.db.GetStorageProofsForHeight(ctx, height)
	if err != nil {
		return fmt.Errorf("error getting storage proofs for height %d: %v", height, err)
	}

	if len(storageProofs) == 0 {
		// No storage proofs submitted for this height
		return nil
	}

	// In the ETL context, we can't do cryptographic verification like the core system does,
	// but we can implement simplified consensus logic based on majority agreement.

	// Count consensus on who the expected provers were
	expectedProvers := make(map[string]int)
	for _, sp := range storageProofs {
		for _, proverAddr := range sp.ProverAddresses {
			expectedProvers[proverAddr]++
		}
	}

	// Determine majority threshold (more than half of submitted proofs)
	majorityThreshold := len(storageProofs) / 2

	// Mark proofs as 'pass' if they submitted and were part of majority consensus
	passedProvers := make(map[string]bool)
	for _, sp := range storageProofs {
		if sp.Address != "" && sp.ProofSignature != nil {
			// This prover submitted a proof - mark as passed
			err = e.db.UpdateStorageProofStatus(ctx, db.UpdateStorageProofStatusParams{
				Status:  "pass",
				Proof:   proof,
				Height:  height,
				Address: sp.Address,
			})
			if err != nil {
				e.logger.Error("error updating storage proof status to pass", zap.Error(err))
			} else {
				passedProvers[sp.Address] = true
			}
		}
	}

	// Insert failed storage proofs for validators who were expected by majority but didn't submit
	for expectedProver, voteCount := range expectedProvers {
		if voteCount > majorityThreshold && !passedProvers[expectedProver] {
			// This validator was expected by majority consensus but didn't submit a proof
			err = e.db.InsertFailedStorageProof(ctx, db.InsertFailedStorageProofParams{
				Height:      height,
				Address:     expectedProver,
				BlockHeight: blockHeight,
				TxHash:      txHash,
				CreatedAt:   pgtype.Timestamp{Time: blockTime, Valid: true},
			})
			if err != nil {
				e.logger.Error("error inserting failed storage proof for", zap.String("address", expectedProver), zap.Error(err))
			}
		}
	}

	e.logger.Debug(
		"Processed storage proof consensus",
		zap.Int64("height", height),
		zap.Int("passed", len(passedProvers)),
		zap.Int("expected", len(expectedProvers)),
	)

	return nil
}
