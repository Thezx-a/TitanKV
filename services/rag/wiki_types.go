package rag

// Wiki page / edge / compile task types for TitanWiki (compiled knowledge base).

type WikiPageType string

const (
	WikiPageEntity     WikiPageType = "entity"
	WikiPageConcept    WikiPageType = "concept"
	WikiPageComparison WikiPageType = "comparison"
	WikiPageSummary    WikiPageType = "summary"
	WikiPageQuery      WikiPageType = "query"
)

// WikiFrontmatter mirrors LLM-Wiki style metadata stored with each page.
type WikiFrontmatter struct {
	Title      string       `json:"title"`
	Slug       string       `json:"slug"`
	Type       WikiPageType `json:"type"`
	Tags       []string     `json:"tags,omitempty"`
	Sources    []string     `json:"sources"` // rag doc_ids
	Confidence string       `json:"confidence,omitempty"` // high|medium|low
	Contested  bool         `json:"contested,omitempty"`
	UpdatedAt  int64        `json:"updated_at"`
	CompileVer int          `json:"compile_version"`
}

// WikiPage is one compiled knowledge page.
type WikiPage struct {
	Frontmatter WikiFrontmatter `json:"frontmatter"`
	Body        string          `json:"body"`    // markdown with [[wikilink]]
	Summary     string          `json:"summary"` // short text for embedding
}

// WikiEdge is a directed relation between page slugs.
type WikiEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rel  string `json:"rel"` // links_to | contradicts | derived_from
}

// CompileTask tracks async wiki compile jobs.
type CompileTask struct {
	TaskID    string `json:"task_id"`
	Col       string `json:"col"`
	DocID     string `json:"doc_id"`
	Status    string `json:"status"` // pending|running|success|failed
	Pages     int    `json:"pages"`
	Edges     int    `json:"edges"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// WikiIndexEntry is one row in wiki:index:{col}.
type WikiIndexEntry struct {
	Slug    string       `json:"slug"`
	Title   string       `json:"title"`
	Type    WikiPageType `json:"type"`
	Summary string       `json:"summary"`
}

// WikiIndexDoc is the collection catalog (LLM Wiki index.md analogue).
type WikiIndexDoc struct {
	Col       string           `json:"col"`
	UpdatedAt int64            `json:"updated_at"`
	Entries   []WikiIndexEntry `json:"entries"`
}

// WikiLogEntry is append-only compile/query audit.
type WikiLogEntry struct {
	Action    string `json:"action"` // compile|delete|query
	Subject   string `json:"subject"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// WikiRawMeta accompanies immutable raw source bytes.
type WikiRawMeta struct {
	SrcID  string `json:"src_id"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}
