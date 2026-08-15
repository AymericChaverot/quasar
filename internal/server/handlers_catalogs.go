package server

import (
	"log"

	"quasar/internal/catalog"
	"quasar/internal/db"
)

// customCatalogs parses every catalogue the operator has enabled, in merge
// order.
//
// A broken one is skipped rather than allowed to fail the page. Nothing is
// validated at this point that was not already validated when it was saved, so
// reaching the log line here means a document was changed out from under the
// check — and the page that lists catalogues is where an operator would find
// out, not the one they were trying to deploy an application from.
func (s *Server) customCatalogs() []catalog.Catalog {
	rows, err := db.ListCatalogs(s.db)
	if err != nil {
		log.Printf("catalog: reading operator catalogues: %v", err)
		return nil
	}
	var out []catalog.Catalog
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		c, err := catalog.Parse(row.Name, row.YAML)
		if err != nil {
			log.Printf("catalog: %q will not parse, leaving it out: %v", row.Name, err)
			continue
		}
		out = append(out, c)
	}
	return out
}
