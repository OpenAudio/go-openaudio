package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	v1storage "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	auds "github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	ctx := context.Background()
	serverAddr := "node3.audiusd.devnet"
	privKeyPath := "./pkg/integration_tests/assets/demo_key.txt"

	sdk := auds.NewOpenAudioSDK(serverAddr)
	if err := sdk.ReadPrivKey(privKeyPath); err != nil {
		log.Fatalf("failed to read private key: %w", err)
	}

	audioFile, err := os.Open("./pkg/integration_tests/assets/anxiety-upgrade.mp3")
	if err != nil {
		log.Fatalf("failed to open file: %v", err)
	}
	defer audioFile.Close()
	audioFileBytes, err := io.ReadAll(audioFile)
	if err != nil {
		log.Fatalf("failed to read file: %v", err)
	}

	// upload the track
	uploadFileRes, err := sdk.Storage.UploadFiles(ctx, &connect.Request[v1storage.UploadFilesRequest]{
		Msg: &v1storage.UploadFilesRequest{
			UserWallet: sdk.Address(),
			Template:   "audio",
			Files: []*v1storage.File{
				{
					Filename: "anxiety-upgrade.mp3",
					Data:     audioFileBytes,
				},
			},
		},
	})
	if err != nil || len(uploadFileRes.Msg.Uploads) != 1 {
		log.Fatalf("failed to upload file on test side: %v", err)
	}

	upload := uploadFileRes.Msg.Uploads[0]

	// get the upload info
	_, err = sdk.Storage.GetUpload(ctx, &connect.Request[v1storage.GetUploadRequest]{
		Msg: &v1storage.GetUploadRequest{
			Id: upload.Id,
		},
	})
	if err != nil {
		log.Fatalf("failed to get upload: %v", err)
	}

	// release the track
	title := "Anxiety Upgrade"
	genre := "Electronic"

	// create ERN track release with upload cid
	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    "audius-devnet",
			From:       sdk.Address(),
			Nonce:      uuid.New().String(),
			Expiration: time.Now().Add(time.Hour).Unix(),
		},
		Messages: []*corev1beta1.Message{
			{
				Message: &corev1beta1.Message_Ern{
					Ern: &ddexv1beta1.NewReleaseMessage{
						MessageHeader: &ddexv1beta1.MessageHeader{
							MessageId: fmt.Sprintf("upload_%s", uuid.New().String()),
							MessageSender: &ddexv1beta1.MessageSender{
								PartyId: &ddexv1beta1.Party_PartyId{
									Dpid: sdk.Address(),
								},
							},
							MessageCreatedDateTime: timestamppb.Now(),
							MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE.Enum(),
						},
						PartyList: []*ddexv1beta1.Party{
							{
								PartyReference: "P_UPLOADER",
								PartyId: &ddexv1beta1.Party_PartyId{
									Dpid: sdk.Address(),
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
														ProprietaryId: upload.OrigFileCid,
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
														Uri: upload.TranscodeResults["320"], // Use transcoded file CID as URI
														HashSum: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File_HashSum{
															Algorithm:    "IPFS",
															HashSumValue: upload.TranscodeResults["320"],
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
					},
				},
			},
		},
	}

	transaction := &corev1beta1.Transaction{
		Envelope: envelope,
	}

	submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transactionv2: transaction,
	}))
	if err != nil {
		log.Fatalf("failed to send tx: %v", err)
	}

	ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
	fmt.Printf("tx receipt: %v", &ernReceipt)
}
