package server

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
)

// generateAudioPreviewForUpload exists for the initial preview impl
// which stored preview CID on the upload record itself.
// This is still expected by client when creating + editing a preview.
// When client is fully using generate_preview endpoint, this can probably go away.
// Returns the cid it generated, empty if the upload has no preview selected.
// Callers are responsible for attesting it: the cid is attested with the rest
// of the upload during transcoding, but an edit that changes the preview start
// runs this again and produces a cid that needs an attestation of its own.
func (ss *MediorumServer) generateAudioPreviewForUpload(ctx context.Context, upload *Upload) (string, error) {
	// if a start time is set, also transcode an audio preview from the full 320kbps downsample
	if upload.SelectedPreview.Valid {
		splitPreview := strings.Split(upload.SelectedPreview.String, "|")
		previewStart := splitPreview[1]

		audioPreview, err := ss.generateAudioPreview(ctx, upload.TranscodeResults["320"], previewStart, upload.ID)
		if err != nil {
			return "", err
		}

		upload.TranscodeResults[upload.SelectedPreview.String] = audioPreview.CID
		if err := ss.crud.Update(upload); err != nil {
			return "", err
		}
		return audioPreview.CID, nil
	}
	return "", nil
}

// generateAudioPreview is the new preview impl which requires only a CID + previewStartSeconds, so that it works with Qm CIDs too.
// It returns an AudioPreview record, and the client can use that to update a track record.
//
// Note: previews are deliberately rendezvous-routed without placement
// context. The HTTP /generate_preview endpoint takes a bare CID and has
// no upload row to draw placement from (especially for legacy Qm CIDs);
// rather than thread placement only through the upload-driven path and
// leave the HTTP path inconsistent, both go through rendezvous.
// uploadID is empty from the bare-CID HTTP path, which has no upload row to
// name; linkOrphanWaveforms fills it in from audio_previews on the next sweep.
func (ss *MediorumServer) generateAudioPreview(ctx context.Context, fileHash string, previewStartSeconds string, uploadID string) (*AudioPreview, error) {

	if !ss.haveInMyBucket(fileHash) {
		_, err := ss.findAndPullBlob(ctx, fileHash, nil)
		if err != nil {
			return nil, err
		}
	}

	// pull to temp file
	temp, err := ss.getKeyToTempFile(fileHash)
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())

	srcPath := temp.Name()
	destPath := strings.TrimSuffix(srcPath, "_320.mp3") + "_320_preview.mp3"

	// generate preview
	cmd := exec.Command("ffmpeg",
		"-y",
		"-i", srcPath,
		"-ss", previewStartSeconds, // set preview start time
		"-t", audioPreviewDuration, // set preview duration
		"-b:a", "320k", // set bitrate to 320k
		"-ar", "48000", // set sample rate to 48000 Hz
		"-f", "mp3", // force output to mp3
		"-vn", // no video
		destPath)

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// replicate to peers
	dest, err := os.Open(destPath)
	if err != nil {
		return nil, err
	}
	defer dest.Close()

	// destPath is handed to the waveform worker below rather than deleted here,
	// so whoever ends up owning it is the one that removes it. Until that
	// transfer is taken, this covers every failure between here and there.
	handedOff := false
	defer func() {
		if !handedOff {
			os.Remove(destPath)
		}
	}()

	previewCid, err := cidutil.ComputeFileCID(dest)
	if err != nil {
		return nil, err
	}

	// Record the preview before handing the blob to peers. The row is what
	// makes the cid attributable -- it is the only link from a preview cid
	// back to its upload -- so publishing the blob first leaves a window where
	// a peer holds a preview that nothing on the network can account for.
	// Creating it first does not close that window on its own, since the row
	// still has to clear consensus, but it stops us widening it deliberately.
	audioPreview := &AudioPreview{
		CID:                 previewCid,
		SourceCID:           fileHash,
		PreviewStartSeconds: previewStartSeconds,
		CreatedBy:           ss.Config.Self.Host,
		CreatedAt:           time.Now(),
	}
	if err := ss.crud.Create(audioPreview); err != nil {
		return nil, err
	}

	if _, err = ss.replicateFileParallel(ctx, previewCid, destPath, nil); err != nil {
		return nil, err
	}

	// Analyze from the file we still have rather than reading the blob back.
	//
	// This is not merely the cheaper path, it is the only one that works here.
	// Previews are rendezvous-routed with no placement, so on a StoreAll node
	// bucketForCID sends them to the archive tier whenever this node's rank for
	// the preview cid is >= ReplicationFactor -- which is most of them, since
	// that cid ranks independently of the track's and of who created it. A job
	// without a local path would then be refused by the archive guard and
	// recorded archive_skipped, so previews would go unanalyzed on exactly the
	// nodes this feature is for.
	//
	// Handing over the file sidesteps that honestly: the guard exists to stop
	// cold-storage retrievals, and there is no retrieval when the bytes are
	// already on local disk. The transcode hook and the replication handoff
	// make the same judgment.
	if ss.Config.WaveformEnabled {
		if ss.enqueueWaveformJob(waveformJob{
			cid:       previewCid,
			uploadID:  uploadID,
			localPath: destPath,
			// nil placement, matching how the preview was replicated above.
		}) {
			handedOff = true
		}
	}

	return audioPreview, nil
}
