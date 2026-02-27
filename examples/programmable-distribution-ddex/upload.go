package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func uploadTrackExample(ctx context.Context, auds *sdk.OpenAudioSDK, handler *GeolocationHandler) error {
	audioFile, err := os.Open("../../pkg/integration_tests/assets/anxiety-upgrade.mp3")
	if err != nil {
		return fmt.Errorf("failed to open audio file: %w", err)
	}
	defer audioFile.Close()

	fileCID, err := hashes.ComputeFileCID(audioFile)
	if err != nil {
		return fmt.Errorf("failed to compute file CID: %w", err)
	}
	audioFile.Seek(0, 0) // Reset file position

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		return fmt.Errorf("failed to marshal upload signature: %w", err)
	}

	uploadSignature, err := common.EthSign(auds.PrivKey(), uploadSigBytes)
	if err != nil {
		return fmt.Errorf("failed to generate upload signature: %w", err)
	}

	// Upload to mediorum
	uploadOpts := &mediorum.UploadOptions{
		Template:          "audio",
		Signature:         uploadSignature,
		WaitForTranscode:  true,
		WaitForFileUpload: true,
		OriginalCID:       fileCID,
	}

	uploads, err := auds.Mediorum.UploadFile(ctx, audioFile, "anxiety-upgrade.mp3", uploadOpts)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	if len(uploads) == 0 {
		return fmt.Errorf("no uploads returned")
	}

	upload := uploads[0]
	if upload.Status != "done" {
		return fmt.Errorf("upload failed: %s", upload.Error)
	}

	// Get the transcoded CID (this is what we use in the ERN)
	transcodedCID := upload.GetTranscodedCID()

	// Create ERN message
	title := "Programmable Distribution Demo"
	genre := "Electronic"

	ernMessage := &ddexv1beta1.NewReleaseMessage{
		MessageHeader: &ddexv1beta1.MessageHeader{
			MessageId: fmt.Sprintf("prog_dist_%s", uuid.New().String()),
			MessageSender: &ddexv1beta1.MessageSender{
				PartyId: &ddexv1beta1.Party_PartyId{
					ProprietaryIds: []*ddexv1beta1.Party_ProprietaryId{
						{
							Namespace: common.OAPNamespace,
							Id:        auds.Address(),
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
					Dpid: auds.Address(),
				},
				PartyName: &ddexv1beta1.Party_PartyName{
					FullName: "Programmable Distribution Demo",
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
						DisplayArtistName:     "Demo Artist",
						VersionType:           "OriginalVersion",
						LanguageOfPerformance: "en",
						SoundRecordingEdition: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition{
							Type: "NonImmersiveEdition",
							ResourceId: &ddexv1beta1.Resource_ResourceId{
								ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
									{
										Namespace:     common.OAPNamespace,
										ProprietaryId: transcodedCID,
									},
								},
							},
							TechnicalDetails: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails{
								TechnicalResourceDetailsReference: "T1",
								DeliveryFile: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile{
									Type:                 "AudioFile",
									AudioCodecType:       "MP3",
									NumberOfChannels:     2,
									SamplingRate:         48.0,
									BitsPerSample:        16,
									IsProvidedInDelivery: true,
									File: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File{
										Uri: transcodedCID,
										HashSum: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File_HashSum{
											Algorithm:    "IPFS",
											HashSumValue: transcodedCID,
										},
										FileSize: 1000000, // Placeholder since we don't know transcoded size
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
						DisplayArtistName:     "Demo Artist",
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

	// Create and send transaction
	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    auds.ChainID(),
			From:       auds.Address(),
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

	submitRes, err := auds.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transactionv2: transaction,
	}))
	if err != nil {
		return fmt.Errorf("failed to send ERN transaction: %w", err)
	}

	ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
	if ernReceipt == nil {
		return fmt.Errorf("failed to get ERN receipt")
	}

	// Store addresses in handler for streaming
	handler.ernAddress = ernReceipt.ErnAddress
	handler.resourceAddresses = ernReceipt.ResourceAddresses
	handler.releaseAddresses = ernReceipt.ReleaseAddresses

	fmt.Printf("ERN Address: %s\n", ernReceipt.ErnAddress)
	fmt.Printf("Resource Address: %s\n", ernReceipt.ResourceAddresses[0])
	fmt.Printf("Release Address: %s\n", ernReceipt.ReleaseAddresses[0])
	fmt.Printf("Transcoded CID: %s\n", transcodedCID)

	return nil
}
