package store

// The document tree of a chat: pages addressed by path, every write a new version. Like the task
// board it is queried and changed through the backend inside one transaction per operation.

// Doc is the current state of a page (a cache of its latest version).
type Doc struct {
	Path        string
	Title       string
	Body        string
	Version     int64
	Author      string // of the latest version
	AuthorOwner bool
	TS          string
	Deleted     bool
}

// DocMeta describes a page without its body.
type DocMeta struct {
	Path    string
	Title   string
	Size    int // bytes of the body
	Version int64
	Author  string
	TS      string
}

// DocVersion is one entry of a page's journal. Deleted marks the version that removed the page.
type DocVersion struct {
	Path        string
	Version     int64
	TS          string
	Author      string
	AuthorOwner bool
	Title       string
	Body        string
	Note        string
	Deleted     bool
	Origin      string
	Lamport     int64
	AuthorPub   string
	Sig         string
	Payload     string
}

// DocsTx is the document tree of one chat inside a transaction.
type DocsTx interface {
	GetDoc(path string) (Doc, bool, error)
	PutDoc(d Doc) error // insert or replace by path
	// ListDocs: live pages at or under prefix ("" = all), by path, without bodies.
	ListDocs(prefix string) ([]DocMeta, error)
	// ListDocBodies: live pages at or under prefix with their bodies.
	ListDocBodies(prefix string) ([]Doc, error)

	AddVersion(v DocVersion) error
	GetVersion(path string, version int64) (DocVersion, bool, error)
	// ListVersions: newest first; before 0 means from the newest.
	ListVersions(path string, before int64, limit int) ([]DocVersion, error)
	// AllVersions: every version of every page, by path and version (for rebuilding the state).
	AllVersions() ([]DocVersion, error)

	// NextLamport advances and returns the chat's logical clock.
	NextLamport() (int64, error)
}

// Docs is the optional part of a backend that keeps document trees.
type Docs interface {
	DoDocs(chatID string, fn func(tx DocsTx) error) error
	ViewDocs(chatID string, fn func(tx DocsTx) error) error
}
