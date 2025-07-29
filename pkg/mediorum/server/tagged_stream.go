package server

import (
	"fmt"
	"io"
)

// taggedStream is a wrapper around an io.ReadSeeker that adds a tag to the beginning of the stream
// it is used to add id3 tags to the beginning of the stream and also to seek to the correct position in the stream
type taggedStream struct {
	tag  []byte
	blob io.ReadSeeker
	pos  int64
}

func (t *taggedStream) Read(p []byte) (int, error) {
	if t.pos < int64(len(t.tag)) {
		n := copy(p, t.tag[t.pos:])
		t.pos += int64(n)
		return n, nil
	}
	n, err := t.blob.Read(p)
	t.pos += int64(n)
	return n, err
}

func (t *taggedStream) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	tagLen := int64(len(t.tag))

	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = t.pos + offset
	case io.SeekEnd:
		blobSize, err := t.blob.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, err
		}
		abs = tagLen + blobSize + offset
	default:
		return 0, fmt.Errorf("invalid seek whence")
	}

	if abs < tagLen {
		t.pos = abs
		t.blob.Seek(0, io.SeekStart)
	} else {
		t.pos = abs
		_, err := t.blob.Seek(abs-tagLen, io.SeekStart)
		if err != nil {
			return 0, err
		}
	}
	return t.pos, nil
}
