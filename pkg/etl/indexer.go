package etl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/etl/db"
	"github.com/AudiusProject/audiusd/pkg/etl/location"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

const (
	TxTypePlay                               = "play"
	TxTypeManageEntity                       = "manage_entity"
	TxTypeValidatorRegistration              = "validator_registration"
	TxTypeValidatorDeregistration            = "validator_deregistration"
	TxTypeValidatorRegistrationLegacy        = "validator_registration_legacy"
	TxTypeSlaRollup                          = "sla_rollup"
	TxTypeValidatorMisbehaviorDeregistration = "validator_misbehavior_deregistration"
	TxTypeStorageProof                       = "storage_proof"
	TxTypeStorageProofVerification           = "storage_proof_verification"
	TxTypeRelease                            = "release"
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

func (etl *ETLService) Run() error {
	dbUrl := etl.dbURL
	if dbUrl == "" {
		return fmt.Errorf("dbUrl environment variable not set")
	}

	err := db.RunMigrations(etl.logger, dbUrl, etl.runDownMigrations)
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

	etl.pool = pool
	etl.db = db.New(pool)

	// Initialize pubsub instances
	etl.blockPubsub = NewPubsub[*db.EtlBlock]()
	etl.playPubsub = NewPubsub[*db.EtlPlay]()

	locationDB, err := location.NewLocationService()
	if err != nil {
		etl.logger.Errorf("error creating location service: %v", err)
		return fmt.Errorf("error creating location service: %v", err)
	}
	etl.logger.Infof("location service initialized successfully")
	etl.locationDB = locationDB

	// Initialize materialized view refresher
	etl.mvRefresher = NewMaterializedViewRefresher(etl.pool, etl.logger)

	// Initialize chain ID from core service
	err = etl.InitializeChainID(context.Background())
	if err != nil {
		etl.logger.Errorf("error initializing chain ID: %v", err)
	}

	etl.logger.Infof("starting etl service")

	if etl.checkReadiness {
		err = etl.awaitReadiness()
		if err != nil {
			etl.logger.Errorf("error awaiting readiness: %v", err)
		}
	}

	ctx := context.Background()
	g, gCtx := errgroup.WithContext(ctx)

	// Start materialized view refresher in errgroup
	g.Go(func() error {
		return etl.mvRefresher.Start(gCtx)
	})

	// Start PostgreSQL notification listener
	g.Go(func() error {
		return etl.startPgNotifyListener(gCtx)
	})

	g.Go(func() error {
		if err := etl.indexBlocks(); err != nil {
			return fmt.Errorf("error indexing blocks: %v", err)
		}

		return nil
	})

	return g.Wait()
}

func (etl *ETLService) awaitReadiness() error {
	etl.logger.Infof("awaiting readiness")
	attempts := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		attempts++
		if attempts > 60 {
			return fmt.Errorf("timed out waiting for readiness")
		}

		res, err := etl.core.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
		if err != nil {
			continue
		}

		if res.Msg.Ready {
			return nil
		}
	}

	return nil
}

func (etl *ETLService) indexBlocks() error {
	for {
		// Get the latest indexed block height
		latestHeight, err := etl.db.GetLatestIndexedBlock(context.Background())
		if err != nil {
			// If no records exist, start from block 1
			if errors.Is(err, pgx.ErrNoRows) {
				if etl.startingBlockHeight > 0 {
					// Start from block 1 (nextHeight will be 1)
					latestHeight = etl.startingBlockHeight - 1
				} else {
					// Start from block 1 (nextHeight will be 1)
					latestHeight = 0
				}
			} else {
				etl.logger.Errorf("error getting latest indexed block: %v", err)
				continue
			}
		}

		// Get the next block
		nextHeight := latestHeight + 1
		block, err := etl.core.GetBlock(context.Background(), connect.NewRequest(&corev1.GetBlockRequest{
			Height: nextHeight,
		}))
		if err != nil {
			etl.logger.Errorf("error getting block %d: %v", nextHeight, err)
			continue
		}

		if block.Msg.Block.Height < 0 {
			continue
		}

		// Insert block first
		err = etl.db.InsertBlock(context.Background(), db.InsertBlockParams{
			ProposerAddress: block.Msg.Block.Proposer,
			BlockHeight:     block.Msg.Block.Height,
			BlockTime:       pgtype.Timestamp{Time: block.Msg.Block.Timestamp.AsTime(), Valid: true},
		})
		if err != nil {
			etl.logger.Errorf("error inserting block %d: %v", nextHeight, err)
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
					insertTxParams.TxType = TxTypePlay
					// Use the first play's user_id as the transaction address
					if len(signedTx.Plays.GetPlays()) > 0 {
						insertTxParams.Address = pgtype.Text{String: signedTx.Plays.GetPlays()[0].UserId, Valid: true}
					}
					// Process plays with batch insert
					plays := signedTx.Plays.GetPlays()
					if len(plays) > 0 {
						// Prepare batch insert parameters
						userIDs := make([]string, len(plays))
						trackIDs := make([]string, len(plays))
						cities := make([]string, len(plays))
						regions := make([]string, len(plays))
						countries := make([]string, len(plays))
						playedAts := make([]pgtype.Timestamp, len(plays))
						blockHeights := make([]int64, len(plays))
						txHashes := make([]string, len(plays))
						listenedAts := make([]pgtype.Timestamp, len(plays))
						recordedAts := make([]pgtype.Timestamp, len(plays))

						for i, play := range plays {
							userIDs[i] = play.UserId
							trackIDs[i] = play.TrackId
							cities[i] = play.City
							regions[i] = play.Region
							countries[i] = play.Country
							playedAts[i] = pgtype.Timestamp{Time: play.Timestamp.AsTime(), Valid: true}
							blockHeights[i] = block.Height
							txHashes[i] = tx.Hash
							listenedAts[i] = pgtype.Timestamp{Time: play.Timestamp.AsTime(), Valid: true}
							recordedAts[i] = pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true}
						}

						// Batch insert all plays
						err = etl.db.InsertPlays(context.Background(), db.InsertPlaysParams{
							Column1:  userIDs,
							Column2:  trackIDs,
							Column3:  cities,
							Column4:  regions,
							Column5:  countries,
							Column6:  playedAts,
							Column7:  blockHeights,
							Column8:  txHashes,
							Column9:  listenedAts,
							Column10: recordedAts,
						})
						if err != nil {
							etl.logger.Errorf("error batch inserting plays: %v", err)
						}
					}

				case *corev1.SignedTransaction_ManageEntity:
					insertTxParams.TxType = TxTypeManageEntity
					me := signedTx.ManageEntity
					insertTxParams.Address = pgtype.Text{String: me.GetSigner(), Valid: true}

					// Insert address first
					err := etl.db.InsertAddress(context.Background(), db.InsertAddressParams{
						Address:              me.GetSigner(),
						PubKey:               nil,
						FirstSeenBlockHeight: pgtype.Int8{Int64: block.Height, Valid: true},
						CreatedAt:            pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						etl.logger.Errorf("error inserting address %s: %v", me.GetSigner(), err)
					}

					err = etl.db.InsertManageEntity(context.Background(), db.InsertManageEntityParams{
						Address:     me.GetSigner(),
						EntityType:  me.GetEntityType(),
						EntityID:    me.GetEntityId(),
						Action:      me.GetAction(),
						Metadata:    pgtype.Text{String: me.GetMetadata(), Valid: me.GetMetadata() != ""},
						Signature:   me.GetSignature(),
						Signer:      me.GetSigner(),
						Nonce:       me.GetNonce(),
						BlockHeight: block.Height,
						TxHash:      tx.Hash,
						CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						etl.logger.Errorf("error inserting manage entity %s: %v", me.GetSigner(), err)
					}

				case *corev1.SignedTransaction_ValidatorRegistration:
					insertTxParams.TxType = TxTypeValidatorRegistrationLegacy
					// Legacy validator registration - no specific table insert needed
				case *corev1.SignedTransaction_ValidatorDeregistration:
					insertTxParams.TxType = TxTypeValidatorMisbehaviorDeregistration
					vd := signedTx.ValidatorDeregistration
					// For deregistration we only have comet address, we'll need to look up eth address
					// For now use comet address, can be improved later
					insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
					err = etl.db.InsertValidatorMisbehaviorDeregistration(context.Background(), db.InsertValidatorMisbehaviorDeregistrationParams{
						CometAddress: vd.CometAddress,
						PubKey:       vd.PubKey,
						BlockHeight:  block.Height,
						TxHash:       tx.Hash,
						CreatedAt:    pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						etl.logger.Errorf("error inserting validator misbehavior deregistration: %v", err)
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
							blockTxCount, err := etl.db.GetBlockTransactionCount(context.Background(), blockHeight)
							if err != nil {
								etl.logger.Debugf("failed to get transaction count for block %d: %v", blockHeight, err)
								continue
							}
							txCount += blockTxCount
						}

						// Calculate time duration from the rollup timestamp and previous rollup
						rollupTime := sr.Timestamp.AsTime()
						var duration float64 = 0

						// Try to get the previous rollup to calculate time difference
						if latestRollup, err := etl.db.GetLatestSlaRollup(context.Background()); err == nil {
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
					rollupId, err := etl.db.InsertSlaRollupReturningId(context.Background(), db.InsertSlaRollupReturningIdParams{
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
						etl.logger.Errorf("error inserting SLA rollup: %v", err)
					} else {
						// Get storage proof challenge statistics for this SLA period
						challengeStats, err := etl.calculateChallengeStatistics(sr.BlockStart, sr.BlockEnd)
						if err != nil {
							etl.logger.Errorf("error calculating challenge statistics: %v", err)
							challengeStats = make(map[string]ChallengeStats) // fallback to empty map
						}

						// Insert SLA node reports with the actual rollup ID and challenge data
						for _, report := range sr.Reports {
							stats := challengeStats[report.Address] // Get challenge stats for this validator

							err = etl.db.InsertSlaNodeReport(context.Background(), db.InsertSlaNodeReportParams{
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
								etl.logger.Errorf("error inserting SLA node report: %v", err)
							}
						}
					}
				case *corev1.SignedTransaction_StorageProof:
					insertTxParams.TxType = TxTypeStorageProof
					sp := signedTx.StorageProof
					insertTxParams.Address = pgtype.Text{String: sp.Address, Valid: true}
					err = etl.db.InsertStorageProof(context.Background(), db.InsertStorageProofParams{
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
						etl.logger.Errorf("error inserting storage proof: %v", err)
					}
				case *corev1.SignedTransaction_StorageProofVerification:
					insertTxParams.TxType = TxTypeStorageProofVerification
					spv := signedTx.StorageProofVerification
					// Storage proof verification doesn't have a specific address, leave as null
					err = etl.db.InsertStorageProofVerification(context.Background(), db.InsertStorageProofVerificationParams{
						Height:      spv.Height,
						Proof:       spv.Proof,
						BlockHeight: block.Height,
						TxHash:      tx.Hash,
						CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						etl.logger.Errorf("error inserting storage proof verification: %v", err)
					} else {
						// Process consensus for this storage proof challenge
						err = etl.processStorageProofConsensus(spv.Height, spv.Proof, block.Height, tx.Hash, block.Timestamp.AsTime())
						if err != nil {
							etl.logger.Errorf("error processing storage proof consensus: %v", err)
						}
					}
				case *corev1.SignedTransaction_Attestation:
					at := signedTx.Attestation
					if vr := at.GetValidatorRegistration(); vr != nil {
						insertTxParams.TxType = TxTypeValidatorRegistration
						insertTxParams.Address = pgtype.Text{String: vr.DelegateWallet, Valid: true}
						err = etl.db.InsertValidatorRegistration(context.Background(), db.InsertValidatorRegistrationParams{
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
							etl.logger.Errorf("error inserting validator registration: %v", err)
						}
						// insert RegisteredValidator record
						err = etl.db.RegisterValidator(context.Background(), db.RegisterValidatorParams{
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
							etl.logger.Errorf("error registering validator: %v", err)
						}
					}
					if vd := at.GetValidatorDeregistration(); vd != nil {
						insertTxParams.TxType = TxTypeValidatorDeregistration
						// For attestation deregistration we only have comet address, need to look up eth address
						// For now use comet address, can be improved later
						insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
						err = etl.db.InsertValidatorDeregistration(context.Background(), db.InsertValidatorDeregistrationParams{
							CometAddress: vd.CometAddress,
							CometPubkey:  vd.PubKey,
							BlockHeight:  block.Height,
							TxHash:       tx.Hash,
						})
						if err != nil {
							etl.logger.Errorf("error inserting validator deregistration: %v", err)
						}
						// insert DeregisteredValidator record
						err = etl.db.DeregisterValidator(context.Background(), db.DeregisterValidatorParams{
							DeregisteredAt: pgtype.Int8{Int64: block.Height, Valid: true},
							UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
							Status:         "deregistered",
							CometAddress:   vd.CometAddress,
						})
						if err != nil {
							etl.logger.Errorf("error deregistering validator: %v", err)
						}
					}
				}

				err = etl.db.InsertTransaction(context.Background(), insertTxParams)
				if err != nil {
					etl.logger.Errorf("error inserting transaction %s: %v", tx.Hash, err)
					return
				}

			}(block.Msg.Block, index)
		}

		wg.Wait()

		// TODO: use pgnotify to publish block and play events to pubsub

		if etl.endingBlockHeight > 0 && block.Msg.Block.Height >= etl.endingBlockHeight {
			etl.logger.Infof("ending block height reached, stopping etl service")
			return nil
		}
	}
}

func (etl *ETLService) startPgNotifyListener(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, etl.dbURL)
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
				etl.logger.Errorf("error unmarshalling block: %v", err)
				continue
			}
			if etl.blockPubsub.HasSubscribers(BlockTopic) {
				etl.blockPubsub.Publish(context.Background(), BlockTopic, block)
			}
		case "new_plays":
			play := &db.EtlPlay{}
			err = json.Unmarshal([]byte(notification.Payload), play)
			if err != nil {
				etl.logger.Errorf("error unmarshalling play: %v", err)
				continue
			}
			if etl.playPubsub.HasSubscribers(PlayTopic) {
				etl.playPubsub.Publish(context.Background(), PlayTopic, play)
			}
		}
	}
}

// calculateChallengeStatistics aggregates storage proof challenge data for validators within a block range
// NOTE: This function may be called before all storage proof data for the block range is available,
// leading to potentially inaccurate pre-calculated statistics. Consider calculating these dynamically
// in the UI instead of storing them in the database.
func (etl *ETLService) calculateChallengeStatistics(blockStart, blockEnd int64) (map[string]ChallengeStats, error) {
	ctx := context.Background()
	stats := make(map[string]ChallengeStats)

	// Use the ETL database method to get challenge statistics with proper status tracking
	results, err := etl.db.GetChallengeStatisticsForBlockRange(ctx, db.GetChallengeStatisticsForBlockRangeParams{
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

func (etl *ETLService) processStorageProofConsensus(height int64, proof []byte, blockHeight int64, txHash string, blockTime time.Time) error {
	ctx := context.Background()

	// Get all storage proofs for this height
	storageProofs, err := etl.db.GetStorageProofsForHeight(ctx, height)
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
			err = etl.db.UpdateStorageProofStatus(ctx, db.UpdateStorageProofStatusParams{
				Status:  "pass",
				Proof:   proof,
				Height:  height,
				Address: sp.Address,
			})
			if err != nil {
				etl.logger.Errorf("error updating storage proof status to pass: %v", err)
			} else {
				passedProvers[sp.Address] = true
			}
		}
	}

	// Insert failed storage proofs for validators who were expected by majority but didn't submit
	for expectedProver, voteCount := range expectedProvers {
		if voteCount > majorityThreshold && !passedProvers[expectedProver] {
			// This validator was expected by majority consensus but didn't submit a proof
			err = etl.db.InsertFailedStorageProof(ctx, db.InsertFailedStorageProofParams{
				Height:      height,
				Address:     expectedProver,
				BlockHeight: blockHeight,
				TxHash:      txHash,
				CreatedAt:   pgtype.Timestamp{Time: blockTime, Valid: true},
			})
			if err != nil {
				etl.logger.Errorf("error inserting failed storage proof for %s: %v", expectedProver, err)
			}
		}
	}

	etl.logger.Debugf("Processed storage proof consensus for height %d: %d proofs passed, %d expected by majority",
		height, len(passedProvers), len(expectedProvers))

	return nil
}
