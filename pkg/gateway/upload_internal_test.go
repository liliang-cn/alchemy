package gateway

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §8.4 is the whole reason this decoder exists: "A 10GB dump that is parsed by
// reading it into a string is a service that dies on the first real customer."
// The end-to-end test proves the bytes arrive; this one proves they were never
// all in memory at once, which is the part an end-to-end test cannot see.
//
// The body is deliberately larger than UploadFrameBytes — several times over,
// and not a whole multiple of it, so the short last frame is exercised too.
func TestTheUploadDecoderNeverHoldsMoreThanAFrame(t *testing.T) {
	const size = 3*UploadFrameBytes + 7919
	payload := bytes.Repeat([]byte("alchemy!"), size/8)

	dec := newChunkDecoder(&sourceBody{
		ReadCloser: io.NopCloser(bytes.NewReader(payload)),
		name:       "dump.sql",
		kind:       alchemyv1.SourceKind_SOURCE_KIND_DDL,
		mediaType:  "application/sql",
	})

	var got []byte
	var frames int
	for {
		var chunk alchemyv1.SourceChunk
		err := dec.Decode(&chunk)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		if n := len(chunk.GetData()); n > UploadFrameBytes {
			t.Fatalf("frame %d carries %d bytes, over the %d byte frame; the decoder is buffering", frames, n, UploadFrameBytes)
		}
		if frames == 0 {
			if chunk.GetName() != "dump.sql" || chunk.GetKind() != alchemyv1.SourceKind_SOURCE_KIND_DDL || chunk.GetMediaType() != "application/sql" {
				t.Errorf("the first frame does not name the source: %+v", &chunk)
			}
		} else if chunk.GetName() != "" {
			t.Errorf("frame %d repeats the metadata; the service reads it from the first frame only", frames)
		}
		got = append(got, chunk.GetData()...)
		frames++
	}

	if frames < 4 {
		t.Errorf("frames = %d for %d bytes; a body several times the frame size arrived in one piece", frames, len(payload))
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("the reassembled body differs from what was sent (%d bytes vs %d)", len(got), len(payload))
	}
}

// An empty body is still a source with a name, and the service is the one that
// decides what to do about it. The decoder must therefore emit the naming
// frame rather than an immediate EOF, which the service would report as "no
// frames" — a message about the gateway's framing rather than about the
// caller's request.
func TestAnEmptyBodyStillNamesTheSource(t *testing.T) {
	dec := newChunkDecoder(&sourceBody{
		ReadCloser: io.NopCloser(strings.NewReader("")),
		name:       "empty.sql",
		kind:       alchemyv1.SourceKind_SOURCE_KIND_DDL,
	})

	var chunk alchemyv1.SourceChunk
	if err := dec.Decode(&chunk); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if chunk.GetName() != "empty.sql" {
		t.Errorf("name = %q, want empty.sql", chunk.GetName())
	}
	if len(chunk.GetData()) != 0 {
		t.Errorf("data = %d bytes, want none", len(chunk.GetData()))
	}
	if err := dec.Decode(&alchemyv1.SourceChunk{}); !errors.Is(err, io.EOF) {
		t.Errorf("second frame: err = %v, want EOF", err)
	}
}
