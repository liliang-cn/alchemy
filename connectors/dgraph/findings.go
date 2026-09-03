package dgraph

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// writeChunks stores the text a citation resolves to.
//
// The vectors are NOT stored, and the report says so rather than the loss being
// discovered. Dgraph has a float-list predicate and no vector index this
// connector could search, so keeping the numbers would be storage that answers
// no question — and §5's obligation is to name what was dropped, not to drop
// nothing.
func (l *Loader) writeChunks(ctx context.Context, batch []sink.Chunk, rep *Report) error {
	if l.opts.SkipChunks {
		return nil
	}
	for _, c := range batch {
		xid := chunkXID(l.opts.RunID, c.Index)
		var b strings.Builder
		b.WriteString(nquad("uid(v)", l.pred(keyXID), literal(xid)))
		b.WriteString(nquad("uid(v)", l.pred(keyRun), literal(l.opts.RunID)))
		b.WriteString(nquad("uid(v)", l.pred(keyKind), literal(kindChunk)))
		b.WriteString(nquad("uid(v)", l.pred(keyIndex), intLit(c.Index)))
		b.WriteString(nquad("uid(v)", l.pred(keySource), literal(c.Source)))
		b.WriteString(nquad("uid(v)", l.pred(keyText), literal(c.Text)))
		b.WriteString(nquad("uid(v)", l.pred(keyStart), intLit(c.Start)))
		b.WriteString(nquad("uid(v)", l.pred(keyEnd), intLit(c.End)))
		if c.Strategy != "" {
			b.WriteString(nquad("uid(v)", l.pred(keyChunking), literal(c.Strategy)))
		}
		if c.Heading != "" {
			b.WriteString(nquad("uid(v)", l.pred(keyHeading), literal(c.Heading)))
		}
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write chunk %d: %w", c.Index, err)
		}
		rep.Chunks++
		if len(c.Vector) > 0 {
			rep.SkippedVectors++
		}
	}
	return nil
}

// writeFindings stores what the job found wrong.
//
// They are nodes and not annotations, and only the duplicates are read back:
// recall.Unanswered is the identity questions, which is what an agent has to be
// able to see. The others are here so that a buyer holding only the store still
// has the numbers §5 obliges the graph to carry — a load whose violations live
// in a JSON file on somebody's laptop is a load you merely have.
func (l *Loader) writeFindings(ctx context.Context, f sink.Findings, rep *Report) error {
	if l.opts.SkipFindings {
		return nil
	}
	n := 0
	next := func(kind string) string {
		n++
		return "alchemy:" + l.opts.RunID + ":" + kind + ":" + strconv.Itoa(n)
	}
	for _, d := range f.Duplicates {
		xid := next(kindDuplicate)
		var b strings.Builder
		b.WriteString(l.findingHead(xid, kindDuplicate))
		b.WriteString(nquad("uid(v)", l.pred(keySignal), literal(string(d.Signal))))
		b.WriteString(nquad("uid(v)", l.pred(keySubject), literal(d.Subject)))
		b.WriteString(nquad("uid(v)", l.pred(keyDetail), literal(d.Detail)))
		// The two names and not the two ids. recall.Question carries names,
		// because the question a person answers is "are these two things the
		// same thing" and an id is not what they recognise it by.
		b.WriteString(nquad("uid(v)", l.pred(keyLeft), literal(d.Left.Name)))
		b.WriteString(nquad("uid(v)", l.pred(keyRight), literal(d.Right.Name)))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write duplicate: %w", err)
		}
		rep.Duplicates++
	}
	for _, v := range f.Violations {
		xid := next(kindViolation)
		var b strings.Builder
		b.WriteString(l.findingHead(xid, kindViolation))
		b.WriteString(nquad("uid(v)", l.pred(keySignal), literal(string(v.Kind))))
		b.WriteString(nquad("uid(v)", l.pred(keySubject), literal(v.Subject)))
		b.WriteString(nquad("uid(v)", l.pred(keyDetail), literal(v.Detail)))
		b.WriteString(l.provenanceQuads("uid(v)", v.Provenance))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write violation: %w", err)
		}
		rep.Violations++
	}
	for _, g := range f.Guesses {
		xid := next(kindGuess)
		var b strings.Builder
		b.WriteString(l.findingHead(xid, kindGuess))
		b.WriteString(nquad("uid(v)", l.pred(keySubject), literal(g.Field)))
		b.WriteString(nquad("uid(v)", l.pred(keyDetail),
			literal(g.ChosenAs+" ("+strings.Join(g.Alternatives, ", ")+"): "+g.Reason)))
		b.WriteString(l.provenanceQuads("uid(v)", g.Provenance))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write guess: %w", err)
		}
		rep.Guesses++
	}
	for _, u := range f.Unread {
		xid := next(kindUnread)
		var b strings.Builder
		b.WriteString(l.findingHead(xid, kindUnread))
		b.WriteString(nquad("uid(v)", l.pred(keySource), literal(u.Source)))
		b.WriteString(nquad("uid(v)", l.pred(keySubject), literal(u.Locator)))
		b.WriteString(nquad("uid(v)", l.pred(keyDetail), literal(u.Reason)))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write unread: %w", err)
		}
		rep.Unread++
	}
	return nil
}

func (l *Loader) findingHead(xid, kind string) string {
	var b strings.Builder
	b.WriteString(nquad("uid(v)", l.pred(keyXID), literal(xid)))
	b.WriteString(nquad("uid(v)", l.pred(keyRun), literal(l.opts.RunID)))
	b.WriteString(nquad("uid(v)", l.pred(keyKind), literal(kind)))
	return b.String()
}

// writeSupersessions records what this result says is over.
//
// Recorded, never applied. This connector is one delete mutation away from
// performing the retirement, and taking it would let one producer remove
// another producer's fact by naming it. alchemy states a retirement and does
// not perform one; what the store owes is that the statement survives the load.
func (l *Loader) writeSupersessions(ctx context.Context, ss []alchemy.Supersession, rep *Report) error {
	for i, s := range ss {
		xid := "alchemy:" + l.opts.RunID + ":supersession:" + strconv.Itoa(i)
		var b strings.Builder
		b.WriteString(l.findingHead(xid, "supersession"))
		b.WriteString(nquad("uid(v)", l.pred(keySubject), literal(s.Retires)))
		b.WriteString(nquad("uid(v)", l.pred(keyDetail), literal(s.Reason)))
		b.WriteString(nquad("uid(v)", l.pred(keyRight), literal(s.By.ID)))
		b.WriteString(l.provenanceQuads("uid(v)", s.Provenance))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write supersession: %w", err)
		}
		rep.Supersessions++
	}
	return nil
}
