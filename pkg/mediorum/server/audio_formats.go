package server

import (
	"fmt"
	"strings"
)

// audioDemuxers are the ffmpeg demuxers an audio upload may be read with.
//
// ffmpeg picks a demuxer by sniffing the bytes, not by the file name, and it
// will happily open far more than audio: playlists, subtitle files, image
// sequences, game video. Transcoding has no business with any of those, so
// both the upload check and the ffmpeg invocations below are held to this
// list. The names are ffmpeg's demuxer names, as ffprobe reports them in
// format_name (one demuxer can report several comma-joined names, e.g.
// "mov,mp4,m4a,3gp,3g2,mj2"; matching any one of them is enough).
//
// To accept a new format, add its demuxer name here.
var audioDemuxers = []string{
	"wav",
	"w64",
	"mp3",
	"flac",
	"aiff",
	"aac",
	"ogg",
	"mov", // mov,mp4,m4a,3gp,3g2,mj2 -- m4a/alac/aac-in-mp4
	"mpc",
	"mpc8",
}

var audioDemuxerSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(audioDemuxers))
	for _, d := range audioDemuxers {
		m[d] = struct{}{}
	}
	return m
}()

// errUnsupportedAudioFormat is returned for a file whose probed format is not
// an audio demuxer we transcode.
type errUnsupportedAudioFormat struct{ format string }

func (e errUnsupportedAudioFormat) Error() string {
	if e.format == "" {
		return "unsupported audio format: could not determine file format"
	}
	return fmt.Sprintf("unsupported audio format: %s", e.format)
}

// checkAudioFormat reports whether ffprobe identified the file as one of
// audioDemuxers.
func checkAudioFormat(probe *FFProbeResult) error {
	if probe == nil {
		return errUnsupportedAudioFormat{}
	}
	name := probe.Format.FormatName
	for _, n := range strings.Split(name, ",") {
		if _, ok := audioDemuxerSet[strings.TrimSpace(n)]; ok {
			return nil
		}
	}
	return errUnsupportedAudioFormat{format: name}
}

// ffmpegAudioInputArgs restricts how ffmpeg may open an audio input: only from
// a local file, and only with one of audioDemuxers. Pass them before "-i".
// They hold even when the format check above was done by another node, since
// ffmpeg enforces them against the demuxer it actually picks.
func ffmpegAudioInputArgs() []string {
	return []string{
		"-protocol_whitelist", "file",
		"-format_whitelist", strings.Join(audioDemuxers, ","),
	}
}

// isAudioTemplate reports whether an upload with this template is transcoded
// as audio. An empty template has always fallen back to audio.
func isAudioTemplate(t JobTemplate) bool {
	return t == JobTemplateAudio || t == ""
}
