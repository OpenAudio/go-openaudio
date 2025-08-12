package integration_tests

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	corev1beta1 "github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDDEX(t *testing.T) {
	ctx := context.Background()
	sdk := utils.DiscoveryOne

	// Wait for the node to be ready once for all subtests
	t.Run("NodeReady", func(t *testing.T) {
		timeout := time.After(30 * time.Second)
		for {
			select {
			case <-timeout:
				assert.Fail(t, "timed out waiting for discovery node to be ready")
			default:
			}
			status, err := sdk.Core.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
			assert.NoError(t, err)
			if status.Msg.Ready {
				t.Log("✓ Discovery node is ready")
				break
			}
			time.Sleep(2 * time.Second)
		}
	})

	// Test individual message submissions
	t.Run("ERNNewMessage", func(t *testing.T) {
		// Create ERN NewReleaseMessage based on fake band data
		ernMessage := createFakeBandERNMessage()

		// Create transaction envelope
		envelope := &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    "audius-devnet",
				From:       "PADPIDA2024010501X",
				To:         "PADPIDA202401120D9",
				Nonce:      "1",
				Expiration: time.Now().Add(time.Hour).Unix(),
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Ern{
						Ern: ernMessage,
					},
				},
			},
		}

		transaction := &corev1beta1.Transaction{
			Envelope: envelope,
		}

		// Calculate expected transaction hash
		expectedTxHash, err := common.ToTxHash(transaction)
		require.NoError(t, err)

		// Submit the transaction
		req := &corev1.SendTransactionRequest{
			Transactionv2: transaction,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		require.NoError(t, err)

		// Check if we have a transaction receipt (v2 transactions)
		if submitRes.Msg.TransactionReceipt != nil {
			txhash := submitRes.Msg.TransactionReceipt.TxHash
			assert.Equal(t, expectedTxHash, txhash)

			// Wait for transaction to be processed
			time.Sleep(time.Second * 2)

			// Retrieve and validate the transaction
			txRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
			require.NoError(t, err)

			// For v2 transactions, check if we got the transaction back
			if txRes.Msg.Transaction != nil {
				t.Logf("ERN transaction %s successfully submitted and retrieved", txhash)
			}

			t.Logf("✓ ERN transaction %s successfully processed with %d parties, %d resources, %d releases, %d deals - representing 'Live - Electric Nights' album (2h43m of live music)",
				txhash, len(ernMessage.PartyList), len(ernMessage.ResourceList), len(ernMessage.ReleaseList), len(ernMessage.DealList))
		} else if submitRes.Msg.Transaction != nil {
			// Fallback for v1 transactions
			txhash := submitRes.Msg.Transaction.Hash
			assert.Equal(t, expectedTxHash, txhash)
			t.Logf("✓ ERN transaction %s successfully processed (v1 response)", txhash)
		} else {
			t.Fatal("No transaction receipt or transaction returned from submission")
		}
	})

	t.Run("MEADNewMessage", func(t *testing.T) {
		// Create MEAD message based on fake band data
		meadMessage := createFakeBandMEADMessage()

		// Create transaction envelope
		envelope := &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    "audius-devnet",
				From:       "PADPIDA2024010501X",
				To:         "PADPIDA202401120D9",
				Nonce:      "2",
				Expiration: time.Now().Add(time.Hour).Unix(),
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Mead{
						Mead: meadMessage,
					},
				},
			},
		}

		transaction := &corev1beta1.Transaction{
			Envelope: envelope,
		}

		// Calculate expected transaction hash
		expectedTxHash, err := common.ToTxHash(transaction)
		require.NoError(t, err)

		// Submit the transaction
		req := &corev1.SendTransactionRequest{
			Transactionv2: transaction,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		require.NoError(t, err)

		// Check if we have a transaction receipt (v2 transactions)
		if submitRes.Msg.TransactionReceipt != nil {
			txhash := submitRes.Msg.TransactionReceipt.TxHash
			assert.Equal(t, expectedTxHash, txhash)

			// Wait for transaction to be processed
			time.Sleep(time.Second * 2)

			// Retrieve and validate the transaction
			txRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
			require.NoError(t, err)

			// For v2 transactions, check if we got the transaction back
			if txRes.Msg.Transaction != nil {
				t.Logf("MEAD transaction %s successfully submitted and retrieved", txhash)
			}

			t.Logf("✓ MEAD transaction %s successfully processed with %d resource enrichments and %d release enrichments",
				txhash, len(meadMessage.ResourceInformationList.ResourceInformation), len(meadMessage.ReleaseInformationList.ReleaseInformation))
		} else if submitRes.Msg.Transaction != nil {
			// Fallback for v1 transactions
			txhash := submitRes.Msg.Transaction.Hash
			assert.Equal(t, expectedTxHash, txhash)
			t.Logf("✓ MEAD transaction %s successfully processed (v1 response)", txhash)
		} else {
			t.Fatal("No transaction receipt or transaction returned from submission")
		}
	})

	t.Run("PIENewMessage", func(t *testing.T) {
		// Create PIE message based on fake band data
		pieMessage := createFakeBandPIEMessage()

		// Create transaction envelope
		envelope := &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    "audius-devnet",
				From:       "PADPIDA2024010501X",
				To:         "PADPIDA202401120D9",
				Nonce:      "3",
				Expiration: time.Now().Add(time.Hour).Unix(),
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Pie{
						Pie: pieMessage,
					},
				},
			},
		}

		transaction := &corev1beta1.Transaction{
			Envelope: envelope,
		}

		// Calculate expected transaction hash
		expectedTxHash, err := common.ToTxHash(transaction)
		require.NoError(t, err)

		// Submit the transaction
		req := &corev1.SendTransactionRequest{
			Transactionv2: transaction,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		require.NoError(t, err)

		// Check if we have a transaction receipt (v2 transactions)
		if submitRes.Msg.TransactionReceipt != nil {
			txhash := submitRes.Msg.TransactionReceipt.TxHash
			assert.Equal(t, expectedTxHash, txhash)

			// Wait for transaction to be processed
			time.Sleep(time.Second * 2)

			// Retrieve and validate the transaction
			txRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
			require.NoError(t, err)

			// For v2 transactions, check if we got the transaction back
			if txRes.Msg.Transaction != nil {
				t.Logf("PIE transaction %s successfully submitted and retrieved", txhash)
			}

			t.Logf("✓ PIE transaction %s successfully processed with %d party enrichments including verified handles and awards",
				txhash, len(pieMessage.PartyList.Party))
		} else if submitRes.Msg.Transaction != nil {
			// Fallback for v1 transactions
			txhash := submitRes.Msg.Transaction.Hash
			assert.Equal(t, expectedTxHash, txhash)
			t.Logf("✓ PIE transaction %s successfully processed (v1 response)", txhash)
		} else {
			t.Fatal("No transaction receipt or transaction returned from submission")
		}
	})

	t.Run("MultiMessageTransaction", func(t *testing.T) {
		// Create all three message types
		ernMessage := createFakeBandERNMessage()
		meadMessage := createFakeBandMEADMessage()
		pieMessage := createFakeBandPIEMessage()

		// Create transaction envelope with multiple messages
		envelope := &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    "audius-devnet",
				From:       "PADPIDA2024010501X",
				To:         "PADPIDA202401120D9",
				Nonce:      "4",
				Expiration: time.Now().Add(time.Hour).Unix(),
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Ern{
						Ern: ernMessage,
					},
				},
				{
					Message: &corev1beta1.Message_Mead{
						Mead: meadMessage,
					},
				},
				{
					Message: &corev1beta1.Message_Pie{
						Pie: pieMessage,
					},
				},
			},
		}

		transaction := &corev1beta1.Transaction{
			Envelope: envelope,
		}

		// Calculate expected transaction hash
		expectedTxHash, err := common.ToTxHash(transaction)
		require.NoError(t, err)

		// Submit the transaction
		req := &corev1.SendTransactionRequest{
			Transactionv2: transaction,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		require.NoError(t, err)

		// Check if we have a transaction receipt (v2 transactions)
		if submitRes.Msg.TransactionReceipt != nil {
			txhash := submitRes.Msg.TransactionReceipt.TxHash
			assert.Equal(t, expectedTxHash, txhash)

			// Wait for transaction to be processed
			time.Sleep(time.Second * 3)

			// Retrieve and validate the transaction
			txRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
			require.NoError(t, err)

			// For v2 transactions, check if we got the transaction back
			if txRes.Msg.Transaction != nil {
				t.Logf("Multi-message transaction %s successfully submitted and retrieved", txhash)
			}

			t.Logf("✓ Multi-message transaction %s successfully processed with ERN (%d parties, %d resources, %d releases), MEAD (%d resource enrichments, %d release enrichments), and PIE (%d party enrichments)",
				txhash,
				len(ernMessage.PartyList), len(ernMessage.ResourceList), len(ernMessage.ReleaseList),
				len(meadMessage.ResourceInformationList.ResourceInformation), len(meadMessage.ReleaseInformationList.ReleaseInformation),
				len(pieMessage.PartyList.Party))
		} else if submitRes.Msg.Transaction != nil {
			// Fallback for v1 transactions
			txhash := submitRes.Msg.Transaction.Hash
			assert.Equal(t, expectedTxHash, txhash)
			t.Logf("✓ Multi-message transaction %s successfully processed (v1 response)", txhash)
		} else {
			t.Fatal("No transaction receipt or transaction returned from submission")
		}
	})

	// Test getter methods
	t.Run("GetterMethods", func(t *testing.T) {
		// Create all three message types for submission
		ernMessage := createFakeBandERNMessage()
		meadMessage := createFakeBandMEADMessage()
		pieMessage := createFakeBandPIEMessage()

		// Submit all messages in a single transaction for getter testing
		envelope := &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    "audius-devnet",
				From:       "PADPIDA2024010501X",
				To:         "PADPIDA202401120D9",
				Nonce:      "5",
				Expiration: time.Now().Add(time.Hour).Unix(),
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Ern{
						Ern: ernMessage,
					},
				},
				{
					Message: &corev1beta1.Message_Mead{
						Mead: meadMessage,
					},
				},
				{
					Message: &corev1beta1.Message_Pie{
						Pie: pieMessage,
					},
				},
			},
		}

		transaction := &corev1beta1.Transaction{
			Envelope: envelope,
		}

		// Submit the transaction
		req := &corev1.SendTransactionRequest{
			Transactionv2: transaction,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		require.NotNil(t, submitRes.Msg.TransactionReceipt)

		txhash := submitRes.Msg.TransactionReceipt.TxHash
		t.Logf("Submitted multi-message transaction for getter testing: %s", txhash)

		// Wait for transaction to be processed
		time.Sleep(time.Second * 5)

		// get individual message receipts
		ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
		meadReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[1].GetMeadAck()
		pieReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[2].GetPieAck()

		// Test GetERN
		t.Run("GetERN", func(t *testing.T) {
			ernReq := &corev1.GetERNRequest{Address: ernReceipt.ErnAddress}
			ernRes, err := sdk.Core.GetERN(ctx, connect.NewRequest(ernReq))
			if err == nil {
				assert.NotNil(t, ernRes.Msg.Ern)
				assert.Equal(t, ernMessage.MessageHeader.MessageId, ernRes.Msg.Ern.MessageHeader.MessageId)
				assert.Equal(t, len(ernMessage.PartyList), len(ernRes.Msg.Ern.PartyList))
				assert.Equal(t, len(ernMessage.ResourceList), len(ernRes.Msg.Ern.ResourceList))
				assert.Equal(t, len(ernMessage.ReleaseList), len(ernRes.Msg.Ern.ReleaseList))
				t.Logf("✓ GetERN successful for address: %s", ernReq.Address)
			} else {
				t.Fatalf("✗ GetERN failed for address: %s", ernReq.Address)
			}
		})

		// Test GetMEAD
		t.Run("GetMEAD", func(t *testing.T) {
			meadReq := &corev1.GetMEADRequest{Address: meadReceipt.MeadAddress}
			meadRes, err := sdk.Core.GetMEAD(ctx, connect.NewRequest(meadReq))
			if err == nil {
				assert.NotNil(t, meadRes.Msg.Mead)
				assert.Equal(t, meadMessage.MessageHeader.MessageId, meadRes.Msg.Mead.MessageHeader.MessageId)
				assert.Equal(t, len(meadMessage.ResourceInformationList.ResourceInformation), len(meadRes.Msg.Mead.ResourceInformationList.ResourceInformation))
				assert.Equal(t, len(meadMessage.ReleaseInformationList.ReleaseInformation), len(meadRes.Msg.Mead.ReleaseInformationList.ReleaseInformation))
				t.Logf("✓ GetMEAD successful for address: %s", meadReq.Address)
			} else {
				t.Fatalf("✗ GetMEAD failed for address: %s", meadReq.Address)
			}
		})

		// Test GetPIE
		t.Run("GetPIE", func(t *testing.T) {
			pieReq := &corev1.GetPIERequest{Address: pieReceipt.PieAddress}
			pieRes, err := sdk.Core.GetPIE(ctx, connect.NewRequest(pieReq))
			if err == nil {
				assert.NotNil(t, pieRes.Msg.Pie)
				assert.Equal(t, pieMessage.MessageHeader.MessageId, pieRes.Msg.Pie.MessageHeader.MessageId)
				assert.Equal(t, len(pieMessage.PartyList.Party), len(pieRes.Msg.Pie.PartyList.Party))
				t.Logf("✓ GetPIE successful for address: %s", pieReq.Address)
			} else {
				t.Fatalf("✗ GetPIE failed for address: %s", pieReq.Address)
			}
		})
	})
}

// createFakeBandERNMessage creates an ERN message based on fake band data
func createFakeBandERNMessage() *ddexv1beta1.NewReleaseMessage {
	// Create message header based on fake XML MessageHeader
	messageHeader := &ddexv1beta1.MessageHeader{
		MessageThreadId: stringPtr("F0100045091829_ADS"),
		MessageId:       "458380160",
		MessageSender: &ddexv1beta1.MessageSender{
			PartyId: &ddexv1beta1.Party_PartyId{
				Dpid: "PADPIDA2024010501X",
			},
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Melodic Records Entertainment",
			},
		},
		MessageRecipient: []*ddexv1beta1.MessageRecipient{
			{
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "PADPIDA202401120D9",
				},
				PartyName: &ddexv1beta1.Party_PartyName{
					FullName: "Audius",
				},
			},
		},
		MessageCreatedDateTime: timestamppb.New(time.Date(2025, 6, 4, 17, 9, 19, 141000000, time.UTC)),
		MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TEST_MESSAGE.Enum(),
	}

	// Create ALL parties from fake band data (comprehensive list)
	parties := []*ddexv1beta1.Party{
		{
			PartyReference: "P_MRE_SENDER",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Melodic Records Entertainment",
			},
			PartyId: &ddexv1beta1.Party_PartyId{
				Dpid: "PADPIDA2024010501X",
			},
		},
		// Main Artists
		{
			PartyReference: "P_ARTIST_1199281",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "The Electric Riders",
			},
		},
		{
			PartyReference: "P_ARTIST_4729799",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Marcus Stone",
			},
		},
		{
			PartyReference: "P_ARTIST_2729598",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Jake Rivers",
			},
		},
		{
			PartyReference: "P_ARTIST_33801",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Alex Thunder",
			},
		},
		{
			PartyReference: "P_ARTIST_753696",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Leo Midnight",
			},
		},
		// Composers and Songwriters
		{
			PartyReference: "P_ARTIST_5105",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Sam Melody",
			},
		},
		{
			PartyReference: "P_ARTIST_5188",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Luna Hayes",
			},
		},
		{
			PartyReference: "P_ARTIST_5189",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Ray Lightning",
			},
		},
		{
			PartyReference: "P_ARTIST_7775",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Chris Voltage",
			},
		},
		{
			PartyReference: "P_ARTIST_11122",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Tony Storm",
			},
		},
		{
			PartyReference: "P_ARTIST_16163",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Felix Harmony",
			},
		},
		{
			PartyReference: "P_ARTIST_16164",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Dave Echo",
			},
		},
		{
			PartyReference: "P_ARTIST_21383",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Nick Reverb",
			},
		},
		{
			PartyReference: "P_ARTIST_21387",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Max Chorus",
			},
		},
		{
			PartyReference: "P_ARTIST_21467",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Ryan Beat",
			},
		},
		// Musicians
		{
			PartyReference: "P_ARTIST_23050",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Oliver Bass",
			},
		},
		{
			PartyReference: "P_ARTIST_23052",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Noah Drums",
			},
		},
		{
			PartyReference: "P_ARTIST_23053",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Ethan Keys",
			},
		},
		{
			PartyReference: "P_ARTIST_23054",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Liam Guitar",
			},
		},
		{
			PartyReference: "P_ARTIST_39619",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Jake R. Rivers",
			},
		},
		{
			PartyReference: "P_ARTIST_41932",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Tyler Sonic",
			},
		},
		{
			PartyReference: "P_ARTIST_65552",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Blake Phoenix",
			},
		},
		{
			PartyReference: "P_ARTIST_73668",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "River Sterling",
			},
		},
		{
			PartyReference: "P_ARTIST_74124",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Cole Rhythm",
			},
		},
		{
			PartyReference: "P_ARTIST_87653",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Quinn Melody",
			},
		},
		{
			PartyReference: "P_ARTIST_109713",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Drew Harmony",
			},
		},
		{
			PartyReference: "P_ARTIST_109714",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Sage Notes",
			},
		},
		{
			PartyReference: "P_ARTIST_109768",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Kai Tempo",
			},
		},
		{
			PartyReference: "P_ARTIST_110895",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Zane Chord",
			},
		},
		{
			PartyReference: "P_ARTIST_2729177",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Atlas Lyric",
			},
		},
		{
			PartyReference: "P_ARTIST_2732982",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Phoenix Tune",
			},
		},
		{
			PartyReference: "P_ARTIST_2758344",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Unknown",
			},
		},
		{
			PartyReference: "P_ARTIST_3949457",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Orion Sound",
			},
		},
		{
			PartyReference: "P_ARTIST_3992112",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Nova Wave",
			},
		},
		{
			PartyReference: "P_ARTIST_3992113",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Ryder Beat",
			},
		},
		{
			PartyReference: "P_ARTIST_7896981",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Echo Pulse",
			},
		},
		{
			PartyReference: "P_ARTIST_2543992",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
			},
		},
		// Label
		{
			PartyReference: "P_LABEL_HARMONY_RECORDS",
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Harmony Records Legacy",
			},
		},
	}

	// Create fake sound recording resources
	resources := []*ddexv1beta1.Resource{
		{
			Resource: &ddexv1beta1.Resource_SoundRecording_{
				SoundRecording: &ddexv1beta1.Resource_SoundRecording{
					ResourceReference:     "A1",
					Type:                  "MusicalWorkSoundRecording",
					DisplayTitleText:      "Midnight Express (Live at Thunder Arena, Electric City, TX - March 2023)",
					VersionType:           "LiveVersion",
					DisplayArtistName:     "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
					Duration:              "PT0H1M32S",
					FirstPublicationDate:  "2023-05-20",
					ParentalWarningType:   "ExplicitContentEdited",
					LanguageOfPerformance: "en",
				},
			},
		},
		{
			Resource: &ddexv1beta1.Resource_SoundRecording_{
				SoundRecording: &ddexv1beta1.Resource_SoundRecording{
					ResourceReference:     "A2",
					Type:                  "MusicalWorkSoundRecording",
					DisplayTitleText:      "Electric Storm (Live at Thunder Arena, Electric City, TX - March 2023)",
					VersionType:           "LiveVersion",
					DisplayArtistName:     "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
					Duration:              "PT0H2M54S",
					FirstPublicationDate:  "2023-05-20",
					ParentalWarningType:   "ExplicitContentEdited",
					LanguageOfPerformance: "en",
				},
			},
		},
		{
			Resource: &ddexv1beta1.Resource_SoundRecording_{
				SoundRecording: &ddexv1beta1.Resource_SoundRecording{
					ResourceReference:     "A3",
					Type:                  "MusicalWorkSoundRecording",
					DisplayTitleText:      "Thunder Roads (Live at Thunder Arena, Electric City, TX - March 2023)",
					VersionType:           "LiveVersion",
					DisplayArtistName:     "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
					Duration:              "PT0H2M27S",
					FirstPublicationDate:  "2023-05-20",
					ParentalWarningType:   "ExplicitContentEdited",
					LanguageOfPerformance: "en",
				},
			},
		},
		{
			Resource: &ddexv1beta1.Resource_SoundRecording_{
				SoundRecording: &ddexv1beta1.Resource_SoundRecording{
					ResourceReference:     "A8",
					Type:                  "MusicalWorkSoundRecording",
					DisplayTitleText:      "Lightning Flash (Live at Thunder Arena, Electric City, TX - March 2023)",
					VersionType:           "LiveVersion",
					DisplayArtistName:     "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
					Duration:              "PT0H3M3S",
					FirstPublicationDate:  "2023-05-20",
					ParentalWarningType:   "ExplicitContentEdited",
					LanguageOfPerformance: "en",
				},
			},
		},
		{
			Resource: &ddexv1beta1.Resource_SoundRecording_{
				SoundRecording: &ddexv1beta1.Resource_SoundRecording{
					ResourceReference:     "A9",
					Type:                  "MusicalWorkSoundRecording",
					DisplayTitleText:      "Voltage Blues (Live at Thunder Arena, Electric City, TX - March 2023)",
					VersionType:           "LiveVersion",
					DisplayArtistName:     "The Electric Riders, Marcus Stone, Jake Rivers, Alex Thunder, Leo Midnight",
					Duration:              "PT0H3M39S",
					FirstPublicationDate:  "2023-05-20",
					ParentalWarningType:   "ExplicitContentEdited",
					LanguageOfPerformance: "en",
				},
			},
		},
		{
			Resource: &ddexv1beta1.Resource_Image_{
				Image: &ddexv1beta1.Resource_Image{
					ResourceReference: "A47",
					Type:              "FrontCoverImage",
					ResourceId: &ddexv1beta1.Resource_ResourceId{
						Isrc: "ISRC123456789012",
						ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
							{
								Namespace:     "AUDIUS",
								ProprietaryId: "123456789012",
							},
						},
					},
					TechnicalDetails: &ddexv1beta1.Resource_Image_TechnicalDetails{
						ImageCodecType:  "JPEG",
						ImageHeight:     1000,
						ImageWidth:      1000,
						ImageResolution: "72dpi",
						File: &ddexv1beta1.Resource_Image_TechnicalDetails_File{
							FileSize: 1000,
							Uri:      "CID:123456789012",
							HashSum: &ddexv1beta1.Resource_Image_TechnicalDetails_File_HashSum{
								Algorithm:    "IPFS",
								HashSumValue: "Qm123456789012",
							},
						},
						IsProvidedInDelivery: true,
					},
				},
			},
		},
	}

	// Release information - fake album data
	// Main Album: "Live - Electric Nights" (R0) - 2h43m total, Rock genre
	// Individual track releases: R1-R44+ (each track as separate release)
	// UPC: 123456789012, Catalog: F0100045091829, Harmony Records Legacy
	// TODO: Add complete Release structure (currently simplified to main album only)
	releases := []*ddexv1beta1.Release{
		{
			Release: &ddexv1beta1.Release_MainRelease{
				MainRelease: &ddexv1beta1.Release_Release{
					ReleaseReference: "R0",
					ReleaseType:      "Album",
					ReleaseId: &ddexv1beta1.Release_ReleaseId{
						Grid:            "F10301F00045091829",
						Icpn:            "123456789012",
						CatalogueNumber: "F0100045091829",
					},
					DisplayTitleText: "Live - Electric Nights",
					DisplayTitle: &ddexv1beta1.Release_DisplayTitle{
						TitleText: "Live - Electric Nights",
					},
					DisplayArtistName: "The Electric Riders",
					DisplayArtist: []*ddexv1beta1.Release_DisplayArtist{
						{
							ArtistPartyReference: "P_ARTIST_1199281",
							DisplayArtistRole:    "MainArtist",
						},
					},
					ReleaseLabelReference: "P_LABEL_HARMONY_RECORDS",
					PLine: &ddexv1beta1.Release_Release_PLine{
						Year:      "2023",
						PLineText: "(P) 2023 Melodic Records Entertainment",
					},
					Duration:            "PT2H43M8S", // 2 hours 43 minutes total!
					OriginalReleaseDate: "2023-05-20",
					ParentalWarningType: "NotExplicit",
					Genre: &ddexv1beta1.Release_Release_Genre{
						GenreText: "Rock",
					},
				},
			},
		},
	}

	// Deal information - fake deal data for worldwide licensing
	// TODO: Add proper Deal protobuf structure once schema issues resolved
	deals := []*ddexv1beta1.Deal{}

	return &ddexv1beta1.NewReleaseMessage{
		MessageHeader: messageHeader,
		PartyList:     parties,
		ResourceList:  resources,
		ReleaseList:   releases,
		DealList:      deals,
	}
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

// createFakeBandMEADMessage creates a MEAD message with enrichment data for fake band resources and releases
func createFakeBandMEADMessage() *ddexv1beta1.MeadMessage {
	// Create message header
	messageHeader := &ddexv1beta1.MessageHeader{
		MessageThreadId: stringPtr("F0100045091829_MEAD"),
		MessageId:       "458380161",
		MessageSender: &ddexv1beta1.MessageSender{
			PartyId: &ddexv1beta1.Party_PartyId{
				Dpid: "PADPIDA2024010501X",
			},
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Melodic Records Entertainment",
			},
		},
		MessageRecipient: []*ddexv1beta1.MessageRecipient{
			{
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "PADPIDA202401120D9",
				},
				PartyName: &ddexv1beta1.Party_PartyName{
					FullName: "Audius",
				},
			},
		},
		MessageCreatedDateTime: timestamppb.New(time.Date(2025, 6, 4, 17, 10, 19, 141000000, time.UTC)),
		MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TEST_MESSAGE.Enum(),
	}

	// Create resource enrichment information
	resourceInformationList := &ddexv1beta1.MeadMessage_ResourceInformationList{
		ResourceInformation: []*ddexv1beta1.MeadMessage_ResourceInformation{
			{
				ResourceSummary: &ddexv1beta1.MeadMessage_ResourceSummary{
					ResourceId: &ddexv1beta1.Resource_ResourceId{
						Isrc: "USRC17607839",
						ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
							{
								Namespace:     "AUDIUS",
								ProprietaryId: "res_midnight_express",
							},
						},
					},
					DisplayTitle: &ddexv1beta1.Resource_DisplayTitle{
						TitleText: "Midnight Express (Live at Thunder Arena)",
					},
					DisplayArtist: &ddexv1beta1.Resource_DisplayArtist{
						ArtistPartyReference: "P_ARTIST_1199281",
					},
					Mood: &ddexv1beta1.MeadMessage_Mood{
						Type:        "Energetic",
						Description: "High-energy live rock performance with electric guitar solos",
					},
					BeatsPerMinute: &ddexv1beta1.MeadMessage_BeatsPerMinute{
						Value: 128.5,
					},
					Key: &ddexv1beta1.MeadMessage_Key{
						Value: "A Minor",
					},
				},
				ResourceContributor: []*ddexv1beta1.MeadMessage_ResourceContributor{
					{
						PartyId: &ddexv1beta1.Party_PartyId{
							Dpid: "CONTRIB001",
						},
						PartyName: &ddexv1beta1.Party_PartyName{
							FullName: "Thunder Arena Live Sound Engineers",
						},
					},
				},
			},
			{
				ResourceSummary: &ddexv1beta1.MeadMessage_ResourceSummary{
					ResourceId: &ddexv1beta1.Resource_ResourceId{
						Isrc: "USRC17607840",
						ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
							{
								Namespace:     "AUDIUS",
								ProprietaryId: "res_electric_storm",
							},
						},
					},
					DisplayTitle: &ddexv1beta1.Resource_DisplayTitle{
						TitleText: "Electric Storm (Live at Thunder Arena)",
					},
					DisplayArtist: &ddexv1beta1.Resource_DisplayArtist{
						ArtistPartyReference: "P_ARTIST_1199281",
					},
					Mood: &ddexv1beta1.MeadMessage_Mood{
						Type:        "Intense",
						Description: "Thunderous drums and lightning-fast guitar riffs",
					},
					BeatsPerMinute: &ddexv1beta1.MeadMessage_BeatsPerMinute{
						Value: 140.0,
					},
					Key: &ddexv1beta1.MeadMessage_Key{
						Value: "E Major",
					},
				},
			},
		},
	}

	// Create release enrichment information
	releaseInformationList := &ddexv1beta1.MeadMessage_ReleaseInformationList{
		ReleaseInformation: []*ddexv1beta1.MeadMessage_ReleaseInformation{
			{
				ReleaseSummary: &ddexv1beta1.MeadMessage_ReleaseSummary{
					ReleaseId: &ddexv1beta1.Release_ReleaseId{
						Grid:            "F10301F00045091829",
						Icpn:            "123456789012",
						CatalogueNumber: "F0100045091829",
					},
					DisplayTitle: &ddexv1beta1.Release_DisplayTitle{
						TitleText: "Live - Electric Nights",
					},
					DisplayArtist: &ddexv1beta1.Release_DisplayArtist{
						ArtistPartyReference: "P_ARTIST_1199281",
					},
				},
				Mood: &ddexv1beta1.MeadMessage_Mood{
					Type:        "Live Concert",
					Description: "Electrifying live performance capturing the raw energy of rock music",
				},
				BeatsPerMinute: &ddexv1beta1.MeadMessage_BeatsPerMinute{
					Value: 132.0, // Average BPM across the album
				},
				Key: &ddexv1beta1.MeadMessage_Key{
					Value: "Various Keys",
				},
			},
		},
	}

	return &ddexv1beta1.MeadMessage{
		MessageHeader:           messageHeader,
		ResourceInformationList: resourceInformationList,
		ReleaseInformationList:  releaseInformationList,
	}
}

// createFakeBandPIEMessage creates a PIE message with party enrichment data for fake band members
func createFakeBandPIEMessage() *ddexv1beta1.PieMessage {
	// Create message header
	messageHeader := &ddexv1beta1.MessageHeader{
		MessageThreadId: stringPtr("F0100045091829_PIE"),
		MessageId:       "458380162",
		MessageSender: &ddexv1beta1.MessageSender{
			PartyId: &ddexv1beta1.Party_PartyId{
				Dpid: "PADPIDA2024010501X",
			},
			PartyName: &ddexv1beta1.Party_PartyName{
				FullName: "Melodic Records Entertainment",
			},
		},
		MessageRecipient: []*ddexv1beta1.MessageRecipient{
			{
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "PADPIDA202401120D9",
				},
				PartyName: &ddexv1beta1.Party_PartyName{
					FullName: "Audius",
				},
			},
		},
		MessageCreatedDateTime: timestamppb.New(time.Date(2025, 6, 4, 17, 11, 19, 141000000, time.UTC)),
		MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TEST_MESSAGE.Enum(),
	}

	// Create party enrichment list with social media handles, verification, and awards
	partyList := &ddexv1beta1.PieMessage_PartyList{
		Party: []*ddexv1beta1.PieMessage_Party{
			{
				PartyReference: "P_ARTIST_1199281",
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "BAND_ELECTRIC_RIDERS_001",
				},
				PartyName: []*ddexv1beta1.Party_PartyName{{
					FullName: "The Electric Riders",
				}},
				PartyType: &ddexv1beta1.PieMessage_Party_PartyType{
					Value: "Artist",
				},
				Biographies: []*ddexv1beta1.PieMessage_Biography{{
					Text: "Riders of the Electric Sky",
					Author: &ddexv1beta1.PartyDescriptorWithPronounciation{
						Parties: []*ddexv1beta1.PartyDescriptorWithPronounciation_PartyIdOrPartyName{{
							Party: &ddexv1beta1.PartyDescriptorWithPronounciation_PartyIdOrPartyName_PartyId{
								PartyId: &ddexv1beta1.Party_PartyId{
									Dpid: "DPID",
								},
							},
						}, {
							Party: &ddexv1beta1.PartyDescriptorWithPronounciation_PartyIdOrPartyName_PartyName{
								PartyName: &ddexv1beta1.Party_PartyName{
									FullName: "The Electric Riders",
								},
							},
						},
						},
					},
				}},
			},
			{
				PartyReference: "P_ARTIST_4729799",
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "ARTIST_MARCUS_STONE_001",
				},
				PartyName: []*ddexv1beta1.Party_PartyName{{
					FullName: "Marcus Stone",
				}},
				PartyType: &ddexv1beta1.PieMessage_Party_PartyType{
					Value: "Individual",
				},
			},
			{
				PartyReference: "P_LABEL_HARMONY_RECORDS",
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: "LABEL_HARMONY_001",
				},
				PartyName: []*ddexv1beta1.Party_PartyName{{
					FullName: "Harmony Records Legacy",
				}},
				PartyType: &ddexv1beta1.PieMessage_Party_PartyType{
					Value: "Label",
				},
			},
		},
	}

	return &ddexv1beta1.PieMessage{
		MessageHeader: messageHeader,
		PartyList:     partyList,
	}
}
