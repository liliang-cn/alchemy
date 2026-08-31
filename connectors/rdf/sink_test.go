package rdf

import (
	"github.com/liliang-cn/alchemy/pkg/recall"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// The two interfaces this connector implements, asserted where a reader looks
// for them. §4.1's argument is that the envelope is one thing written four
// times; this is the fifth store agreeing to it, and the first that had a read
// path from the day it was written rather than added afterwards.
var (
	_ sink.Sink     = (*Loader)(nil)
	_ recall.Reader = (*Loader)(nil)
)
