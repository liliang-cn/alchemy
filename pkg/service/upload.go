package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
)

// UploadSource spools a corpus to disk.
//
// §8.4 is the whole design of this method: the received bytes are written to a
// file as they arrive and never accumulated, because a 10GB dump assembled in
// memory is a service that dies on the first real customer. Nothing here holds
// more than one frame at a time, and what CreateJob is later handed is a path.
//
// The metadata rides on the first frame rather than in a separate handshake
// message. A handshake would need its own message type and a rule about what
// happens when the second message is another handshake; reading the fields off
// the first frame and ignoring them afterwards has neither, and a client that
// repeats them is not made wrong by doing so.
func (s *Server) UploadSource(stream grpc.ClientStreamingServer[alchemyv1.SourceChunk, alchemyv1.Source]) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err == io.EOF {
		return wireError(invalid("upload: no frames; a source with no bytes is not a source"))
	}
	if err != nil {
		return wireError(err)
	}
	if first.GetName() == "" {
		// §5b: every fact names its source. A source with no name produces
		// provenance nobody can follow, so it is refused at the door rather
		// than discovered at the end of an import.
		return wireError(invalid("upload: the first frame must name the source"))
	}
	kind, ok := wire.SourceKindFromProto[first.GetKind()]
	if !ok {
		return wireError(invalid("upload: the first frame must say which of tabular, ddl, document or graph this is"))
	}

	id := mintID()
	path := filepath.Join(s.spool, id)
	f, err := os.Create(path)
	if err != nil {
		return wireError(err)
	}
	// Removed on every failure path. A spool that keeps the half of a corpus
	// that arrived before a client died is §5c's abandoned-review problem in a
	// directory instead of a map.
	done := false
	defer func() {
		f.Close()
		if !done {
			os.Remove(path)
		}
	}()

	size, err := spool(stream, f, first.GetData(), ctx.Err)
	if err != nil {
		return wireError(err)
	}
	if err := f.Sync(); err != nil {
		return wireError(err)
	}

	src := Source{
		ID: id, Kind: kind, Name: first.GetName(),
		Path: path, Size: size, MediaType: first.GetMediaType(),
	}
	s.mu.Lock()
	s.sources[id] = src
	s.mu.Unlock()
	done = true

	return stream.SendAndClose(&alchemyv1.Source{
		Id: src.ID, Kind: wire.SourceKindToProto[src.Kind], Name: src.Name,
		Size: src.Size, MediaType: src.MediaType,
	})
}

// spool copies the stream to w, one frame at a time.
//
// The cancellation check is per frame rather than per byte: a client that
// disappears mid-upload should stop the write on the next frame, and checking
// more often than that would cost an atomic load per kilobyte to save
// microseconds on a call that is measured in minutes.
func spool(stream grpc.ClientStreamingServer[alchemyv1.SourceChunk, alchemyv1.Source], w io.Writer, first []byte, cancelled func() error) (int64, error) {
	var size int64
	write := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if size+int64(len(b)) > defaultUploadLimit {
			return invalid("upload: source exceeds the %d byte limit", int64(defaultUploadLimit))
		}
		n, err := w.Write(b)
		size += int64(n)
		return err
	}
	if err := write(first); err != nil {
		return size, err
	}
	for {
		if err := cancelled(); err != nil {
			return size, err
		}
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return size, nil
		}
		if err != nil {
			return size, err
		}
		if err := write(msg.GetData()); err != nil {
			return size, err
		}
	}
}

// mintID makes an unguessable identifier, for the same reason pkg/job does: a
// sequential one tells every caller how much the service has handled and lets
// them ask about the upload before theirs.
func mintID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("service: cannot mint an ID: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// sourcesFor resolves the IDs a CreateJob named. An unknown one is refused
// rather than skipped: a job that quietly imports three of the four files it
// was given produces a graph whose gaps nobody can explain.
func (s *Server) sourcesFor(ids []string) ([]Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Source, 0, len(ids))
	for _, id := range ids {
		src, ok := s.sources[id]
		if !ok {
			return nil, invalid("create: no uploaded source %q", id)
		}
		out = append(out, src)
	}
	return out, nil
}

// forget removes a job's spooled sources. §4: the service returns its output
// and forgets it, and a directory that only grows is the slow way of becoming
// the database this is not.
func (s *Server) forget(sources []Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		delete(s.sources, src.ID)
		os.Remove(src.Path)
	}
}
