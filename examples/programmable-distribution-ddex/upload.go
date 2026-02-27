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

func uploadTrackExample(ctx context.Context, auds *sdk.OpenAudioSDK) ([]string, error) {
	audioFile, err := os.Open("../../pkg/integration_tests/assets/anxiety-upgrade.mp3")
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer audioFile.Close()

	fileCID, err := hashes.ComputeFileCID(audioFile)
	if err != nil {
		return nil, fmt.Errorf("failed to compute file CID: %w", err)
	}
	audioFile.Seek(0, 0)

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upload signature: %w", err)
	}

	uploadSignature, err := common.EthSign(auds.PrivKey(), uploadSigBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate upload signature: %w", err)
	}

	uploadOpts := &mediorum.UploadOptions{
		Template:          "audio",
		Signature:         uploadSignature,
		WaitForTranscode:  true,
		WaitForFileUpload: true,
		OriginalCID:       fileCID,
	}

	uploads, err := auds.Mediorum.UploadFile(ctx, audioFile, "anxiety-upgrade.mp3", uploadOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}
	if len(uploads) == 0 {
		return nil, fmt.Errorf("no uploads returned")
	}

	upload := uploads[0]
	if upload.Status != "done" {
		return nil, fmt.Errorf("upload failed: %s", upload.Error)
	}

	transcodedCID := upload.GetTranscodedCID()

	// Wait for FileUpload tx to be committed (ERN validation requires CID in core_uploads)
	for i := 0; i < 30; i++ {
		resp, err := auds.Core.GetUploadByCID(ctx, connect.NewRequest(&corev1.GetUploadByCIDRequest{Cid: transcodedCID}))
		if err == nil && resp.Msg.Exists {
			break
		}
		if i == 29 {
			return nil, fmt.Errorf("FileUpload tx not found after 30s - ensure programmable distribution is enabled")
		}
		time.Sleep(1 * time.Second)
	}

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
						DisplayTitleText:      "Programmable Distribution Demo",
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
										FileSize: 1000000,
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
						DisplayTitleText:      "Programmable Distribution Demo",
						DisplayArtistName:     "Demo Artist",
						ReleaseLabelReference: "P_UPLOADER",
						OriginalReleaseDate:   time.Now().Format("2006-01-02"),
						ParentalWarningType:   "NotExplicit",
						Genre: &ddexv1beta1.Release_Release_Genre{
							GenreText: "Electronic",
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

	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    auds.ChainID(),
			From:       auds.Address(),
			Nonce:      upload.ID,
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
		return nil, fmt.Errorf("failed to send ERN transaction: %w", err)
	}

	receipt := submitRes.Msg.TransactionReceipt
	if receipt == nil {
		return nil, fmt.Errorf("no transaction receipt returned")
	}
	if len(receipt.MessageReceipts) == 0 {
		return nil, fmt.Errorf("no message receipts in transaction")
	}

	var ernReceipt *ddexv1beta1.NewReleaseMessageAck
	for _, mr := range receipt.MessageReceipts {
		if mr != nil {
			ernReceipt = mr.GetErnAck()
			if ernReceipt != nil {
				break
			}
		}
	}
	if ernReceipt == nil {
		return nil, fmt.Errorf("failed to get ERN receipt (tx included but no ERN ack in %d receipts)", len(receipt.MessageReceipts))
	}

	addresses := []string{ernReceipt.ErnAddress}
	addresses = append(addresses, ernReceipt.ResourceAddresses...)
	addresses = append(addresses, ernReceipt.ReleaseAddresses...)

	return addresses, nil
}
