package integration_tests

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUploadStream(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, utils.WaitForDevnetHealthy(30*time.Second))

	serverAddr := "node3.audiusd.devnet"
	privKeyPath := "./assets/demo_key.txt"
	privKeyPath2 := "./assets/demo_key2.txt"

	oap := sdk.NewOpenAudioSDK(serverAddr)
	if err := oap.ReadPrivKey(privKeyPath); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}

	// Initialize SDK to get chain ID
	if err := oap.Init(ctx); err != nil {
		require.Nil(t, err, "failed to initialize SDK: %w", err)
	}

	audioFile, err := os.Open("./assets/anxiety-upgrade.mp3")
	require.Nil(t, err, "failed to open file")
	defer audioFile.Close()

	// release the track
	title := "Anxiety Upgrade"
	genre := "Electronic"

	// Create ERN message
	ernMessage := &ddexv1beta1.NewReleaseMessage{
		MessageHeader: &ddexv1beta1.MessageHeader{
			MessageId: fmt.Sprintf("upload_%s", uuid.New().String()),
			MessageSender: &ddexv1beta1.MessageSender{
				PartyId: &ddexv1beta1.Party_PartyId{
					ProprietaryIds: []*ddexv1beta1.Party_ProprietaryId{
						{
							Namespace: common.OAPNamespace,
							Id:        oap.Address(), // Must match upload signature address
						},
					},
				},
			},
			MessageCreatedDateTime: timestamppb.Now(),
			MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE.Enum(),
		},
		PartyList: []*ddexv1beta1.Party{
			{
				PartyReference: "P_UPLOADER",
				PartyId: &ddexv1beta1.Party_PartyId{
					Dpid: oap.Address(),
				},
				PartyName: &ddexv1beta1.Party_PartyName{
					FullName: "Test Uploader",
				},
			},
		},
		ResourceList: []*ddexv1beta1.Resource{
			{
				Resource: &ddexv1beta1.Resource_SoundRecording_{
					SoundRecording: &ddexv1beta1.Resource_SoundRecording{
						ResourceReference:     "A1",
						Type:                  "MusicalWorkSoundRecording",
						DisplayTitleText:      title,
						DisplayArtistName:     "Test Artist",
						VersionType:           "OriginalVersion",
						LanguageOfPerformance: "en",
						SoundRecordingEdition: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition{
							Type: "NonImmersiveEdition",
							ResourceId: &ddexv1beta1.Resource_ResourceId{
								ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
									{
										Namespace:     "audius",
										ProprietaryId: "{{TRANSCODED_CID}}", // Will be replaced by SDK
									},
								},
							},
							TechnicalDetails: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails{
								TechnicalResourceDetailsReference: "T1",
								DeliveryFile: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile{
									Type:                 "AudioFile",
									AudioCodecType:       "MP3",
									NumberOfChannels:     2,
									SamplingRate:         48.0, // 48kHz as per transcoding
									BitsPerSample:        16,
									IsProvidedInDelivery: true,
									File: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File{
										Uri: "{{TRANSCODED_CID}}", // Will be replaced by SDK
										HashSum: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File_HashSum{
											Algorithm:    "IPFS",
											HashSumValue: "{{TRANSCODED_CID}}", // Will be replaced by SDK
										},
										FileSize: 1000000, // Placeholder file size
									},
								},
								IsClip: false,
							},
						},
					},
				},
			},
		},
		ReleaseList: []*ddexv1beta1.Release{
			{
				Release: &ddexv1beta1.Release_MainRelease{
					MainRelease: &ddexv1beta1.Release_Release{
						ReleaseReference:      "R1",
						ReleaseType:           "Single",
						DisplayTitleText:      title,
						DisplayArtistName:     "Test Artist",
						ReleaseLabelReference: "P_UPLOADER",
						OriginalReleaseDate:   time.Now().Format("2006-01-02"),
						ParentalWarningType:   "NotExplicit",
						Genre: &ddexv1beta1.Release_Release_Genre{
							GenreText: genre,
						},
						ResourceGroup: &ddexv1beta1.Release_Release_ResourceGroup{
							ResourceGroup: []*ddexv1beta1.Release_Release_ResourceGroup_ResourceGroup{
								{
									ResourceGroupType: "Audio",
									SequenceNumber:    "1",
									ResourceGroupContentItem: []*ddexv1beta1.Release_Release_ResourceGroup_ResourceGroup_ResourceGroupContentItem{
										{
											ResourceGroupContentItemType: "Track",
											ResourceGroupContentItemText: "A1",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Upload options
	uploadOpts := &mediorum.UploadOptions{
		Template: "audio",
	}

	// Step 1: Upload file via mediorum
	t.Log("Starting file upload...")

	// Compute CID and generate upload signature
	fileCID, err := hashes.ComputeFileCID(audioFile)
	require.NoError(t, err, "failed to compute file CID")
	audioFile.Seek(0, 0) // Reset file position

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	require.NoError(t, err, "failed to marshal upload signature")

	uploadSignature, err := common.EthSign(oap.PrivKey(), uploadSigBytes)
	require.NoError(t, err, "failed to generate upload signature")

	uploadOpts.Signature = uploadSignature
	uploadOpts.WaitForTranscode = true
	uploadOpts.WaitForFileUpload = true
	uploadOpts.OriginalCID = fileCID

	uploads, err := oap.Mediorum.UploadFile(ctx, audioFile, "anxiety-upgrade.mp3", uploadOpts)
	require.NoError(t, err, "failed to upload file")
	require.NotEmpty(t, uploads, "no uploads returned")

	upload := uploads[0]
	require.Equal(t, "done", upload.Status, "upload failed: %s", upload.Error)

	transcodedCID := upload.GetTranscodedCID()
	t.Logf("File uploaded successfully!")
	t.Logf("Original CID: %s", upload.OrigFileCID)
	t.Logf("Transcoded CID: %s", transcodedCID)

	// Step 2: Replace placeholder CIDs in ERN message with actual transcoded CID
	for _, resource := range ernMessage.ResourceList {
		if soundRecording := resource.GetSoundRecording(); soundRecording != nil {
			if edition := soundRecording.SoundRecordingEdition; edition != nil {
				if resourceId := edition.ResourceId; resourceId != nil {
					for _, propId := range resourceId.ProprietaryId {
						if propId.ProprietaryId == "{{TRANSCODED_CID}}" {
							propId.ProprietaryId = transcodedCID
						}
					}
				}
				if techDetails := edition.TechnicalDetails; techDetails != nil {
					if deliveryFile := techDetails.DeliveryFile; deliveryFile != nil {
						if file := deliveryFile.File; file != nil {
							if file.Uri == "{{TRANSCODED_CID}}" {
								file.Uri = transcodedCID
							}
							if hashSum := file.HashSum; hashSum != nil && hashSum.HashSumValue == "{{TRANSCODED_CID}}" {
								hashSum.HashSumValue = transcodedCID
							}
						}
					}
				}
			}
		}
	}

	// Step 3: Send ERN transaction via core
	t.Log("Sending ERN transaction...")
	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    oap.ChainID(),
			From:       oap.Address(),
			Nonce:      upload.ID, // Use upload ID as nonce
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

	transaction := &corev1beta1.Transaction{Envelope: envelope}

	submitRes, err := oap.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transactionv2: transaction,
	}))
	require.NoError(t, err, "failed to send ERN transaction")

	ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
	require.NotNil(t, ernReceipt, "failed to get ERN receipt")

	t.Log("ERN transaction completed!")
	t.Logf("ERN created at address: %s", ernReceipt.ErnAddress)
	t.Logf("Resource addresses: %v", ernReceipt.ResourceAddresses)
	t.Logf("Release addresses: %v", ernReceipt.ReleaseAddresses)

	// Test streaming different entity types
	// 1. Stream by ERN address (gets all resources)
	// 2. Stream by specific resource address
	// 3. Stream by release address (gets all resources in release)

	// Wait a moment for indexing
	time.Sleep(2 * time.Second)

	// Create stream signature for requesting stream URLs
	streamExpiry := time.Now().Add(1 * time.Hour)
	addressesToStream := []string{
		ernReceipt.ErnAddress,           // ERN address - returns all resources
		ernReceipt.ResourceAddresses[0], // Specific resource address
		ernReceipt.ReleaseAddresses[0],  // Release address - returns resources in release
	}

	streamSigData := &corev1.GetStreamURLsSignature{
		Addresses: addressesToStream,
		ExpiresAt: timestamppb.New(streamExpiry),
	}
	streamSigBytes, err := proto.Marshal(streamSigData)
	require.Nil(t, err, "failed to marshal stream signature data")

	streamSignature, err := common.EthSign(oap.PrivKey(), streamSigBytes)
	require.Nil(t, err, "failed to generate stream signature")

	// Request stream URLs from core
	streamReq := &corev1.GetStreamURLsRequest{
		Signature: streamSignature,
		Addresses: addressesToStream,
		ExpiresAt: timestamppb.New(streamExpiry),
	}

	streamRes, err := oap.Core.GetStreamURLs(ctx, connect.NewRequest(streamReq))
	if err != nil {
		t.Logf("GetStreamURLs error: %v", err)
		t.Logf("GetStreamURLs error details: %+v", err)
	}
	require.Nil(t, err, "failed to get stream URLs")
	require.NotNil(t, streamRes.Msg.EntityStreamUrls, "no stream URLs returned")

	// Log all stream URLs for manual testing
	t.Log("=== STREAM URLS FOR MANUAL TESTING ===")
	for address, entityUrls := range streamRes.Msg.EntityStreamUrls {
		t.Logf("\nEntity Address: %s", address)
		t.Logf("  Type: %s", entityUrls.EntityType)
		t.Logf("  Reference: %s", entityUrls.EntityReference)
		t.Logf("  Parent ERN: %s", entityUrls.ErnAddress)
		for i, url := range entityUrls.Urls {
			t.Logf("  Stream URL %d: %s", i+1, url)
			t.Log("  You can test this URL with: curl -I \"" + url + "\"")
		}
	}
	t.Log("=======================================")

	// Verify we got URLs for all requested addresses
	require.Len(t, streamRes.Msg.EntityStreamUrls, 3, "should have URLs for all 3 requested addresses")

	// Test ERN address returns stream URLs
	ernUrls := streamRes.Msg.EntityStreamUrls[ernReceipt.ErnAddress]
	require.NotNil(t, ernUrls, "should have URLs for ERN address")
	require.Equal(t, "ern", ernUrls.EntityType)
	require.NotEmpty(t, ernUrls.Urls, "ERN should have stream URLs")
	t.Logf("ERN returned %d stream URLs", len(ernUrls.Urls))

	// Test resource address returns stream URLs
	resourceUrls := streamRes.Msg.EntityStreamUrls[ernReceipt.ResourceAddresses[0]]
	require.NotNil(t, resourceUrls, "should have URLs for resource address")
	require.Equal(t, "resource", resourceUrls.EntityType)
	require.NotEmpty(t, resourceUrls.Urls, "Resource should have stream URLs")
	require.Equal(t, ernReceipt.ErnAddress, resourceUrls.ErnAddress, "Resource should reference parent ERN")
	t.Logf("Resource returned %d stream URLs", len(resourceUrls.Urls))

	// Test release address returns stream URLs
	releaseUrls := streamRes.Msg.EntityStreamUrls[ernReceipt.ReleaseAddresses[0]]
	require.NotNil(t, releaseUrls, "should have URLs for release address")
	require.Equal(t, "release", releaseUrls.EntityType)
	require.NotEmpty(t, releaseUrls.Urls, "Release should have stream URLs")
	require.Equal(t, ernReceipt.ErnAddress, releaseUrls.ErnAddress, "Release should reference parent ERN")
	t.Logf("Release returned %d stream URLs", len(releaseUrls.Urls))

	// Test that non-owner cannot get stream URLs
	t.Log("\n=== Testing access control ===")
	sdk2 := sdk.NewOpenAudioSDK(serverAddr)
	if err := sdk2.ReadPrivKey(privKeyPath2); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}

	// Try to stream with different wallet (should fail)
	wrongStreamSig, err := common.EthSign(sdk2.PrivKey(), streamSigBytes)
	require.Nil(t, err, "failed to generate wrong stream signature")

	wrongStreamReq := &corev1.GetStreamURLsRequest{
		Signature: wrongStreamSig,
		Addresses: addressesToStream,
		ExpiresAt: timestamppb.New(streamExpiry),
	}

	wrongStreamRes, err := sdk2.Core.GetStreamURLs(ctx, connect.NewRequest(wrongStreamReq))
	require.Error(t, err, "non-owner should not be able to get stream URLs")
	require.Nil(t, wrongStreamRes, "should not return stream URLs for non-owner")
	t.Log("✓ Access control working: non-owner rejected")

	// Test expired signature
	expiredSigData := &corev1.GetStreamURLsSignature{
		Addresses: addressesToStream,
		ExpiresAt: timestamppb.New(time.Now().Add(-1 * time.Hour)), // Already expired
	}
	expiredSigBytes, err := proto.Marshal(expiredSigData)
	require.Nil(t, err, "failed to marshal expired signature data")

	expiredSig, err := common.EthSign(oap.PrivKey(), expiredSigBytes)
	require.Nil(t, err, "failed to generate expired signature")

	expiredReq := &corev1.GetStreamURLsRequest{
		Signature: expiredSig,
		Addresses: addressesToStream,
		ExpiresAt: timestamppb.New(time.Now().Add(-1 * time.Hour)),
	}

	expiredRes, err := oap.Core.GetStreamURLs(ctx, connect.NewRequest(expiredReq))
	require.Error(t, err, "expired signature should be rejected")
	require.Nil(t, expiredRes, "should not return stream URLs for expired signature")
	t.Log("✓ Expired signature rejected")
}
