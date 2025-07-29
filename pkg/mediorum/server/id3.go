package server

import (
	"bytes"
	"encoding/binary"
)

func buildID3v2Tag(title, artist string) []byte {
	frames := filterNilFrames([][]byte{
		createTextFrame("TIT2", title),
		createTextFrame("TPE1", artist),
	})

	body := bytes.Join(frames, nil)

	header := &bytes.Buffer{}
	header.WriteString("ID3")
	header.Write([]byte{3, 0})        // ID3v2.3.0
	header.WriteByte(0)               // flags
	header.Write(syncSafe(len(body))) // tag size in sync-safe format
	header.Write(body)
	return header.Bytes()
}

func createTextFrame(id, value string) []byte {
	if value == "" {
		return nil
	}
	content := append([]byte{0}, []byte(value)...) // encoding: 0 = ISO-8859-1
	size := uint32(len(content))

	buf := &bytes.Buffer{}
	buf.WriteString(id)
	binary.Write(buf, binary.BigEndian, size)
	buf.Write([]byte{0, 0}) // flags
	buf.Write(content)
	return buf.Bytes()
}

func syncSafe(size int) []byte {
	return []byte{
		byte((size >> 21) & 0x7F),
		byte((size >> 14) & 0x7F),
		byte((size >> 7) & 0x7F),
		byte(size & 0x7F),
	}
}

func filterNilFrames(frames [][]byte) [][]byte {
	var out [][]byte
	for _, f := range frames {
		if f != nil {
			out = append(out, f)
		}
	}
	return out
}
