// Command measure runs the Northgate evaluation against a deployed alchemy and
// writes what it found to docs/claims/, where internal/claims checks it.
//
// Every source kind the product supports, each under the part of the ontology
// it belongs to, against the caller's own models. Nothing is synthetic except
// the ontology and the inventory CSV, which are the two things a buyer writes.
//
// It is in the repository rather than in somebody's shell history for the
// reason the Makefile's generate target is: DESIGN.md §9 makes claims about
// what a real corpus does, and a claim whose command cannot be re-run is a
// claim nobody can check. The corpus itself is not committed -- it is Northgate's
// published schema, documentation and code, and the file below says where each
// piece came from -- so this takes the directory holding it as an argument.
//
//	ALCHEMY_TOKEN=... go run ./cmd/measure <addr> <fixtures-dir>
//
// The fixtures directory holds ontology.json plus the six source files named
// in jobs below. Every credential comes from the environment -- the bearer
// token, and the same four CPA_* and T2M_* variables the service itself reads
// -- so none of them is ever on the command line where ps would show it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type job struct {
	name  string
	files []file
	part  string
	// ontology is sent unless empty: a DDL-only job legitimately has none (§5
	// requires one for document sources), and running one that way proves the
	// "an ontology nobody claimed has no rules to break" rule on real input.
	withOntology bool
}
type file struct {
	name string
	kind v1.SourceKind
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("usage: ALCHEMY_TOKEN=... measure <addr> <fixtures-dir>")
		os.Exit(2)
	}
	addr, dir := os.Args[1], os.Args[2]
	token := os.Getenv("ALCHEMY_TOKEN")
	if token == "" {
		fmt.Println("ALCHEMY_TOKEN is not set; it is read from the environment rather than " +
			"taken as an argument so that ps cannot show it")
		os.Exit(2)
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20)))
	must(err)
	defer cc.Close()
	c := v1.NewAlchemyClient(cc)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	ont, err := os.ReadFile(dir + "/ontology.json")
	must(err)

	jobs := []job{
		{"ddl (no ontology)", []file{{"freight-schema.sql", v1.SourceKind_SOURCE_KIND_DDL}}, "", false},
		{"ddl (governed)", []file{{"freight-schema.sql", v1.SourceKind_SOURCE_KIND_DDL}}, "tabular", true},
		{"tabular", []file{{"inventory.csv", v1.SourceKind_SOURCE_KIND_TABULAR}}, "tabular", true},
		{"document (md)", []file{{"ravel-docs.md", v1.SourceKind_SOURCE_KIND_DOCUMENT}}, "prose", true},
		{"document (pdf)", []file{{"northgate-profile.pdf", v1.SourceKind_SOURCE_KIND_DOCUMENT}}, "prose", true},
		{"graph", []file{{"service-code-graph.json", v1.SourceKind_SOURCE_KIND_GRAPH}}, "code", true},
	}

	fmt.Printf("%-20s %-10s %7s %8s %6s %6s %5s %5s %6s %6s\n",
		"job", "state", "ents", "rels", "viol", "confl", "dup", "guess", "unread", "empty")
	fmt.Println(strings.Repeat("-", 96))
	bad := 0
	values := map[string]float64{}
	kinds := map[v1.SourceKind]bool{}
	for _, j := range jobs {
		st, cn, spend, err := run(ctx, c, dir, ont, j)
		if err != nil {
			fmt.Printf("%-20s ERROR %v\n", j.name, trunc(err.Error(), 70))
			bad++
			continue
		}
		fmt.Printf("%-20s %-10s %7d %8d %6d %6d %5d %5d %6d %6d   %s\n",
			j.name, short(st), cn.GetEntities(), cn.GetRelations(), cn.GetViolations(),
			cn.GetConflicts(), cn.GetDuplicates(), cn.GetGuesses(),
			cn.GetChunksUnread(), cn.GetChunksEmpty(), spend)
		if st != v1.JobState_JOB_STATE_SUCCEEDED || cn.GetConflicts() != 0 {
			bad++
		}
		for _, f := range j.files {
			kinds[f.kind] = true
		}
		// Per job as well as in total. A total of zero conflicts is the claim,
		// but a reader who sees it move wants to know which source moved it,
		// and a total is the one number that cannot say.
		k := strings.NewReplacer(" ", "_", "(", "", ")", "").Replace(j.name)
		values[k+".conflicts"] = float64(cn.GetConflicts())
		values[k+".entities"] = float64(cn.GetEntities())
		values[k+".relations"] = float64(cn.GetRelations())
		values[k+".violations"] = float64(cn.GetViolations())
		values["total.conflicts"] += float64(cn.GetConflicts())
		values["total.violations"] += float64(cn.GetViolations())
		if st == v1.JobState_JOB_STATE_SUCCEEDED {
			values["jobs.succeeded"]++
		}
	}
	values["jobs.run"] = float64(len(jobs))
	values["source_kinds"] = float64(len(kinds))
	fmt.Println(strings.Repeat("-", 96))

	// The file is written whether or not the suite passed, and it records what
	// happened rather than what was wanted. A measurement that is only written
	// when it says the right thing is not a measurement.
	must(writeClaim(values, addr))

	if bad > 0 {
		fmt.Printf("FAILED: %d job(s) did not finish clean\n", bad)
		os.Exit(1)
	}
	fmt.Println("every source kind imported, no job held, no conflicts")
}

// claimPath is where internal/claims looks. Relative to the module root, which
// is where this is expected to be run from.
const claimPath = "docs/claims/evaluation-suite.json"

// writeClaim records the run. Provenance is "measured" because this function
// only runs at the end of one -- the field's whole purpose is that a file
// found on disk without it is not trusted, so nothing else in this repository
// may write that word into a claim file.
func writeClaim(values map[string]float64, addr string) error {
	doc := map[string]any{
		"claim": "DESIGN.md §9: every source kind imported from one real corpus, " +
			"no job held, no conflicts",
		"where":       "DESIGN.md §9",
		"provenance":  "measured",
		"measured_at": time.Now().UTC().Format("2006-01-02"),
		"how": "ALCHEMY_TOKEN=... go run ./cmd/measure " + addr + " <fixtures-dir> " +
			"-- the fixtures are a customer's published schema, documentation, " +
			"code graph and company profile, plus an ontology and an inventory CSV " +
			"written for the evaluation",
		"values": values,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(claimPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(claimPath, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", claimPath)
	return nil
}

func run(ctx context.Context, c v1.AlchemyClient, dir string, ont []byte, j job) (v1.JobState, *v1.Counts, string, error) {
	var ids []string
	for _, f := range j.files {
		body, err := os.ReadFile(dir + "/" + f.name)
		if err != nil {
			return 0, nil, "", err
		}
		st, err := c.UploadSource(ctx)
		if err != nil {
			return 0, nil, "", err
		}
		const frame = 1 << 20
		for off := 0; off < len(body); off += frame {
			end := min(off+frame, len(body))
			ch := &v1.SourceChunk{Data: body[off:end]}
			if off == 0 {
				ch.Name, ch.Kind = f.name, f.kind
			}
			if err := st.Send(ch); err != nil {
				return 0, nil, "", err
			}
		}
		src, err := st.CloseAndRecv()
		if err != nil {
			return 0, nil, "", err
		}
		ids = append(ids, src.GetId())
	}
	req := &v1.CreateJobRequest{
		SourceIds: ids, Part: j.part,
		Models: &v1.Models{
			Llm:      &v1.ModelEndpoint{Name: os.Getenv("CPA_MODEL"), Endpoint: os.Getenv("CPA_BASE_URL"), ApiKey: os.Getenv("CPA_API_KEY")},
			Embedder: &v1.ModelEndpoint{Name: os.Getenv("T2M_EMBED_MODEL"), Endpoint: os.Getenv("T2M_BASE_URL"), ApiKey: os.Getenv("T2M_API_KEY")},
		},
		Chunking: &v1.Chunking{Strategy: "heading", Size: 700},
	}
	if j.withOntology {
		req.Ontology = string(ont)
	}
	start := time.Now()
	jb, err := c.CreateJob(ctx, req)
	if err != nil {
		return 0, nil, "", err
	}
	for time.Since(start) < 12*time.Minute {
		jb, err = c.GetJob(ctx, &v1.GetJobRequest{JobId: jb.GetId()})
		if err != nil {
			return 0, nil, "", err
		}
		s := jb.GetState()
		if s != v1.JobState_JOB_STATE_PENDING && s != v1.JobState_JOB_STATE_RUNNING {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if jb.GetState() != v1.JobState_JOB_STATE_SUCCEEDED {
		return jb.GetState(), &v1.Counts{}, jb.GetError(), nil
	}
	cn, spend := summary(ctx, c, jb.GetId())
	return jb.GetState(), cn, fmt.Sprintf("%s %s", time.Since(start).Round(time.Second), spend), nil
}

// summary reads the counts, paging when the result does not fit one message.
func summary(ctx context.Context, c v1.AlchemyClient, id string) (*v1.Counts, string) {
	if res, err := c.GetResult(ctx, &v1.GetResultRequest{JobId: id}); err == nil {
		return res.GetCounts(), spendOf(res.GetModelCalls())
	}
	st, err := c.StreamResult(ctx, &v1.GetResultRequest{JobId: id})
	if err != nil {
		return &v1.Counts{}, ""
	}
	var cn *v1.Counts
	var calls []*v1.ModelCall
	for {
		p, err := st.Recv()
		if err != nil {
			break
		}
		if p.GetCounts() != nil {
			cn = p.GetCounts()
		}
		if len(p.GetModelCalls()) > 0 {
			calls = p.GetModelCalls()
		}
	}
	if cn == nil {
		cn = &v1.Counts{}
	}
	return cn, spendOf(calls) + " (streamed)"
}

func spendOf(calls []*v1.ModelCall) string {
	if len(calls) == 0 {
		return "no model"
	}
	parts := make([]string, 0, len(calls))
	for _, m := range calls {
		parts = append(parts, fmt.Sprintf("%s:%d", m.GetStage(), m.GetCalls()))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func short(s v1.JobState) string { return strings.TrimPrefix(s.String(), "JOB_STATE_") }
func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func must(err error) {
	if err != nil {
		fmt.Println("ERR:", err)
		os.Exit(1)
	}
}
