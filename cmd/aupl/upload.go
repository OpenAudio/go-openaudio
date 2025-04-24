package main

import (
	"fmt"

	"github.com/spf13/cobra"

	core_sdk "github.com/AudiusProject/audiusd/pkg/core/sdk"
	"github.com/AudiusProject/audiusd/pkg/sdk"
)

var (
	privKeyPath string
	filePath    string
	title       string
	genre       string
	serverAddr  string
	outputPath  string
	trackId     string
)

var uploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload a file with metadata and signature",
	RunE: func(cmd *cobra.Command, args []string) error {
		// upload audio file
		storageSdk := sdk.NewStorageSDK(fmt.Sprintf("https://%s", serverAddr))
		uploadRes, err := storageSdk.UploadAudio(filePath)
		if err != nil || len(uploadRes) == 0 {
			return fmt.Errorf("failed to upload file: %w", err)
		}
		upload := uploadRes[0]

		// release audio file
		coreSdk, err := core_sdk.NewSdk(core_sdk.WithOapiendpoint(serverAddr), core_sdk.WithPrivKeyPath(privKeyPath))
		if err != nil {
			return fmt.Errorf("failed to connect to gRPC server: %w", err)
		}
		res, err := coreSdk.ReleaseTrack(upload.OrigFileCID, title, genre)
		if err != nil {
			return fmt.Errorf("failed to release track: %w", err)
		}

		fmt.Printf("Upload successful\nTrackId: %s\nTxHash: %s", res.TrackID, res.TxHash)
		return nil
	},
}

var downloadCmd = &cobra.Command{
	Use:   "download",
	Short: "Download a file with metadata and signature",
	RunE: func(cmd *cobra.Command, args []string) error {
		storageSdk := sdk.NewStorageSDK(fmt.Sprintf("https://%s", serverAddr))
		if err := storageSdk.LoadPrivateKey(privKeyPath); err != nil {
			return fmt.Errorf("Failed to load private key: %w", err)
		}

		if err := storageSdk.DownloadTrack(trackId, outputPath); err != nil {
			return fmt.Errorf("Failed to download track: %w", err)
		}

		fmt.Printf("Download successful: saved to %s\n", outputPath)
		return nil
	},
}

func init() {
	uploadCmd.Flags().StringVar(&privKeyPath, "key", "", "Path to Ethereum private key file")
	uploadCmd.Flags().StringVar(&filePath, "file", "", "Path to file to upload")
	uploadCmd.Flags().StringVar(&title, "title", "", "Title of the upload")
	uploadCmd.Flags().StringVar(&genre, "genre", "", "Genre of the upload")
	uploadCmd.Flags().StringVar(&serverAddr, "server", "", "server address")
	uploadCmd.MarkFlagRequired("key")
	uploadCmd.MarkFlagRequired("file")

	rootCmd.AddCommand(uploadCmd)

	downloadCmd.Flags().StringVar(&privKeyPath, "key", "", "Path to Ethereum private key file")
	downloadCmd.Flags().StringVar(&trackId, "id", "", "Track ID to download")
	downloadCmd.Flags().StringVar(&serverAddr, "server", "", "server address")
	downloadCmd.Flags().StringVar(&outputPath, "out", "track.mp3", "Path to save the downloaded file")
	downloadCmd.MarkFlagRequired("key")
	downloadCmd.MarkFlagRequired("id")

	rootCmd.AddCommand(downloadCmd)

}

var rootCmd = &cobra.Command{Use: "upload"}

func Execute() error {
	return rootCmd.Execute()
}
