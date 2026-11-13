package watch

import "maps"

// saveCrawlSchedule persists retries without claiming that a page was collected.
// In particular a failed first request must leave Initialized false.
func (s *Store) saveCrawlSchedule(source string, state *CrawlState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	next.Sources = maps.Clone(s.data.Sources)
	checkpoint := next.Sources[source]
	checkpoint.Crawl = cloneCrawl(state)
	next.Sources[source] = checkpoint
	b, err := marshalSourceState(next)
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, b); err != nil {
		return err
	}
	s.data = next
	return nil
}
