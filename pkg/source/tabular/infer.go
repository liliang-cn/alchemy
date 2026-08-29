package tabular

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// inferStage is the stage name in the cost report (DESIGN.md §7.2: cost is not
// optimised for, but it is never hidden).
const inferStage = "tabular-mapping"

// sampleRows is how many rows the model is shown. A header alone does not
// distinguish an identifier from a quantity, and a whole table is neither
// affordable nor necessary; the sample is also the only part of the table held
// in memory beyond one record (§8.4).
const sampleRows = 5

// proposal is the model's reply. Every field is a column name that must appear
// verbatim in the header — validate() enforces that, because a mapping naming a
// column the table does not have is not a mapping of this table.
type proposal struct {
	EntityType string            `json:"entity_type"`
	IDColumn   string            `json:"id_column"`
	NameColumn string            `json:"name_column"`
	Attributes map[string]string `json:"attributes"`
	Relations  []struct {
		Column       string `json:"column"`
		RelationType string `json:"relation_type"`
		TargetType   string `json:"target_type"`
	} `json:"relations"`
	Confidence float64           `json:"confidence"`
	Reasons    map[string]string `json:"reasons"`
}

const inferSystem = `You map the columns of a table onto a graph. Reply with JSON only.

Reply with exactly this shape:
{"entity_type": "Order",
 "id_column": "id",
 "name_column": "",
 "attributes": {"<column>": "<attribute name>"},
 "relations": [{"column": "<column>", "relation_type": "PLACED_BY", "target_type": "Customer"}],
 "confidence": 0.0,
 "reasons": {"id_column": "why this column and not the others", "<column>": "why this role"}}

Rules:
- Every column name you use must appear verbatim in the header you are given.
  Do not invent, correct, abbreviate or re-case a column name.
- id_column is the column that identifies the row itself. A column named after
  another kind of thing — customer_id in a table of orders — identifies that
  other thing, so it is a relation, not the id.
- Put each column in exactly one of id_column, name_column, attributes or
  relations. A column you cannot place, leave out of all four.
- name_column is what a person would call the row. Leave it "" if the table has
  no such column; an id is not a name.
- reasons must say, for each decision, what else that column could have been.
- confidence is your confidence in the mapping as a whole, from 0 to 1.`

// inferMapping asks the model what the columns mean. It returns the model's
// reply alongside the Mapping because the reply carries the reasons, and a
// guess without a reason is a line in a queue nobody can act on.
func inferMapping(ctx context.Context, source string, head []string, samples []record, opts Options) (*Mapping, proposal, alchemy.ModelCall, error) {
	resp, err := opts.LLM.Complete(ctx, alchemy.LLMRequest{
		System: inferSystem,
		Prompt: inferPrompt(source, head, samples, opts.EntityHint),
		JSON:   true,
	})
	call := alchemy.ModelCall{Model: opts.LLM.Name(), Stage: inferStage, Calls: 1, Tokens: resp.Tokens}
	if err != nil {
		return nil, proposal{}, call, fmt.Errorf("tabular: %s: inferring a mapping: %w", source, err)
	}
	var p proposal
	if err := json.Unmarshal([]byte(jsonBody(resp.Text)), &p); err != nil {
		return nil, proposal{}, call, fmt.Errorf("tabular: %s: the model's mapping is not JSON: %w", source, err)
	}
	if strings.TrimSpace(p.EntityType) == "" && opts.EntityHint != "" {
		// The caller stated what a row is, and the model did not. §2.1's first
		// lesson: a statement beats an inference, so the hint stands rather than
		// the table being refused over a field the caller already supplied.
		p.EntityType = opts.EntityHint
		if p.Reasons == nil {
			p.Reasons = map[string]string{}
		}
		p.Reasons["entity_type"] = fmt.Sprintf("the model named no entity type, so the caller's hint %q was used unchanged", opts.EntityHint)
	}
	m := &Mapping{
		EntityType: strings.TrimSpace(p.EntityType),
		IDColumn:   strings.TrimSpace(p.IDColumn),
		NameColumn: strings.TrimSpace(p.NameColumn),
		Attributes: p.Attributes,
	}
	for _, r := range p.Relations {
		m.Relations = append(m.Relations, RelationMapping{
			Column:       strings.TrimSpace(r.Column),
			RelationType: strings.TrimSpace(r.RelationType),
			TargetType:   strings.TrimSpace(r.TargetType),
		})
	}
	return m, p, call, nil
}

// inferPrompt shows the header in order and a few rows under it.
func inferPrompt(source string, head []string, samples []record, hint string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s\n", source)
	if hint != "" {
		fmt.Fprintf(&b, "The caller believes a row is a: %s\n", hint)
	}
	b.WriteString("\nColumns, in the order they appear:\n")
	for i, h := range head {
		fmt.Fprintf(&b, "%2d. %s\n", i+1, h)
	}
	if len(samples) > 0 {
		b.WriteString("\nFirst rows, as columns above:\n")
		for _, rec := range samples {
			b.WriteString("  " + strings.Join(rec.fields, " | ") + "\n")
		}
	}
	return b.String()
}

// jsonBody tolerates a model that wrapped its JSON in prose or a code fence.
// The alternative is failing a whole table over a decoration.
func jsonBody(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return text
	}
	return text[start : end+1]
}
