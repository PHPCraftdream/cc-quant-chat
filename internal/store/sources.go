package store

// Sources is implemented by a backend that keeps which sources a neighbor has brought.
type Sources interface {
	// NoteSource records that events of a source came through a neighbor. A source seen through that
	// neighbor before is allowed; a new one is allowed (and recorded) while the neighbor has brought
	// fewer than limit sources, and not otherwise. fresh says that it was recorded now.
	NoteSource(origin, via string, limit int) (allowed, fresh bool, err error)
	// SourcesVia lists the sources a neighbor has brought.
	SourcesVia(via string) ([]string, error)
}
