package document

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/chunk"
)

// messyHTML is what a real page looks like: uppercase tags, unclosed <p> and
// <li>, an inline <script> and <style>, a comment, entities, attributes with
// angle brackets in them, and inline markup inside a heading.
const messyHTML = `<!DOCTYPE html>
<html lang="en"><head>
<META charset="utf-8">
<title>Ignored chrome</title>
<style type="text/css">
body { font-family: "Helvetica"; } h1::after { content: "not text"; }
</style>
<script>
var leak = "<h1>SCRIPT HEADING</h1>"; if (a < b && c > d) { document.write("nope"); }
</script>
</head>
<BODY>
<!-- a comment that is not text -->
<h1 id="top">Alchemy &amp; the <em>graph</em></h1>
<p>First paragraph with an entity: caf&eacute; &#8212; and a <a href="/x?a=1&amp;b=2">link</a>.
<p>Second paragraph.<br>After a break.
<h2>Chunking</h2>
<ul>
<li>strategies are the caller&rsquo;s choice
<li>cost is not a constraint
</ul>
<table><tr><td>left<td>right</tr></table>
<script src="/analytics.js"></script>
</BODY></html>
`

func TestHTMLBecomesTextWithHeadingsChunkCanSee(t *testing.T) {
	res, err := Read(context.Background(), "page.html", strings.NewReader(messyHTML), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	text := res.Text

	// pkg/chunk finds sections by scanning the text it is given, so the proof
	// that the heading structure survived is that the chunker still finds it.
	chunks, err := chunk.Split(context.Background(), "page.html", text, chunk.Options{
		Strategy: chunk.Heading, MaxTokens: 200, Overlap: chunk.NoOverlap,
	})
	if err != nil {
		t.Fatalf("chunk.Split: %v", err)
	}
	var headings []string
	for _, c := range chunks {
		if c.Heading != "" {
			headings = append(headings, c.Heading)
		}
	}
	want := []string{"Alchemy & the graph", "Chunking"}
	for _, w := range want {
		found := false
		for _, h := range headings {
			if h == w {
				found = true
			}
		}
		if !found {
			t.Errorf("pkg/chunk did not see heading %q; it saw %v\ntext:\n%s", w, headings, text)
		}
	}
}

func TestHTMLScriptAndStyleAreNotText(t *testing.T) {
	res, err := Read(context.Background(), "page.html", strings.NewReader(messyHTML), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, forbidden := range []string{
		"SCRIPT HEADING", "document.write", "var leak",
		"font-family", "Helvetica", "not text",
		"a comment that is not text",
		"analytics.js", "href", "lang=", "charset",
	} {
		if strings.Contains(res.Text, forbidden) {
			t.Errorf("script/style/markup leaked into text: %q\ntext:\n%s", forbidden, res.Text)
		}
	}
}

func TestHTMLProseAndEntitiesSurvive(t *testing.T) {
	res, err := Read(context.Background(), "page.html", strings.NewReader(messyHTML), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{
		"First paragraph with an entity: café — and a link.",
		"Second paragraph.",
		"After a break.",
		"strategies are the caller’s choice",
		"cost is not a constraint",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("missing prose %q\ntext:\n%s", want, res.Text)
		}
	}
	// Blocks must not run together into one word soup.
	if strings.Contains(res.Text, "graphFirst") || strings.Contains(res.Text, "break.After") {
		t.Errorf("block boundaries were lost:\n%s", res.Text)
	}
}

// <pre> is where a page keeps its code, and code whose newlines and indentation
// have been collapsed into single spaces is not code any more.
func TestHTMLPreformattedTextKeepsItsLayout(t *testing.T) {
	const src = `<html><body><h2>Example</h2>
<pre><code>func main() {
    fmt.Println("hi")
}
</code></pre>
<p>After the block.</p></body></html>`
	res, err := Read(context.Background(), "code.html", strings.NewReader(src), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(res.Text, "func main() {\n    fmt.Println(\"hi\")\n}") {
		t.Errorf("preformatted layout was collapsed:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "After the block.") {
		t.Errorf("prose after the block was lost:\n%s", res.Text)
	}
}
