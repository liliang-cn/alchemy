package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"runtime"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// bigSource is deliberately larger than any buffer a reasonable person would
// size for a document: §8.4 says a 10GB dump that is parsed by reading it into
// a string is a service that dies on the first real customer, and a test that
// uploads 4KB would pass against exactly that service.
const (
	bigSource = 48 << 20
	frameSize = 256 << 10
)

func TestUploadSourceSpoolsToDiskWithoutHoldingIt(t *testing.T) {
	srv, cli := serve(t, harness{})

	stream, err := cli.UploadSource(authed(context.Background()))
	if err != nil {
		t.Fatalf("UploadSource: %v", err)
	}

	// The first frame carries the metadata; the rest carry only bytes.
	frame := make([]byte, frameSize)
	sum := sha256.New()
	sent := 0
	for i := 0; sent < bigSource; i++ {
		fill(frame, byte(i))
		msg := &alchemyv1.SourceChunk{Data: frame}
		if i == 0 {
			msg.Name = "dump.sql"
			msg.Kind = alchemyv1.SourceKind_SOURCE_KIND_DDL
			msg.MediaType = "application/sql"
		}
		if err := stream.Send(msg); err != nil {
			t.Fatalf("send frame %d: %v", i, err)
		}
		sum.Write(frame)
		sent += len(frame)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	got, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	if got.GetId() == "" {
		t.Error("no source ID: a CreateJob has nothing to name")
	}
	if got.GetSize() != int64(sent) {
		t.Errorf("size = %d, want %d", got.GetSize(), sent)
	}
	if got.GetKind() != alchemyv1.SourceKind_SOURCE_KIND_DDL {
		t.Errorf("kind = %v, want DDL", got.GetKind())
	}

	src, ok := srv.SourceForTest(got.GetId())
	if !ok {
		t.Fatal("server does not know the source it just returned")
	}
	f, err := os.Open(src.Path)
	if err != nil {
		t.Fatalf("spooled file: %v", err)
	}
	defer f.Close()
	spooled := sha256.New()
	if _, err := io.Copy(spooled, f); err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if !bytes.Equal(spooled.Sum(nil), sum.Sum(nil)) {
		t.Error("spooled bytes differ from what was sent")
	}

	// The heap must not have grown by anything like the size of the upload. A
	// service that assembled the corpus in memory passes every other assertion
	// here and fails this one, which is the whole point of the test.
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > bigSource/4 {
		t.Errorf("heap grew by %d bytes over a %d byte upload; the corpus is being held in memory", grew, bigSource)
	}
}

// A source with no name cannot be reported in provenance, and a fact whose
// source cannot be named is the one thing §5b promises never to return.
func TestUploadSourceRefusesAnUnnamedSource(t *testing.T) {
	cli := dial(t, harness{})
	stream, err := cli.UploadSource(authed(context.Background()))
	if err != nil {
		t.Fatalf("UploadSource: %v", err)
	}
	if err := stream.Send(&alchemyv1.SourceChunk{Data: []byte("x")}); err != nil && err != io.EOF {
		t.Fatalf("send: %v", err)
	}
	_, err = stream.CloseAndRecv()
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

func fill(b []byte, seed byte) {
	for i := range b {
		b[i] = seed + byte(i)
	}
}
