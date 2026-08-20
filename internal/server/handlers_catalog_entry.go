package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"quasar/internal/catalog"
	"quasar/internal/db"
)

// The other way to write a catalogue: one entry at a time, in a form, for an
// operator who has no interest in learning a YAML schema to add the image they
// run. It edits the same document the textarea does — the form is read out of
// it and written back into it — so neither way is the real one and a catalogue
// can be started in the form and finished in the text, or the other way round.

// handleCatalogStart creates an empty catalogue, which is what the form editor
// needs before it has anywhere to put an entry.
//
// It skips the checks a pasted document goes through, and deliberately: an
// empty catalogue fails "declares no entries", which is the right thing to say
// about a document somebody wrote and the wrong thing to say about one they
// have not written yet.
func (s *Server) handleCatalogStart(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		redirectCatalogs(w, r, "A catalogue needs a name.")
		return
	}
	empty := catalog.Catalog{Name: name}
	doc, err := empty.YAML()
	if err != nil {
		redirectCatalogs(w, r, "Could not start that: "+err.Error())
		return
	}
	row := &db.Catalog{Name: name, YAML: doc, Enabled: true}
	id, err := db.InsertCatalog(s.db, row)
	if err != nil {
		redirectCatalogs(w, r, "Could not start that: "+err.Error())
		return
	}
	s.audit(r, "catalog.create", name, "empty")
	http.Redirect(w, r, "/settings/catalogs/"+strconv.FormatInt(id, 10)+"/entries/new", http.StatusSeeOther)
}

// entryContext resolves the catalogue and the entry a request is about. The
// entry is nil for "new", and for an id the catalogue does not hold — which is
// the same thing from the form's point of view.
func (s *Server) entryContext(r *http.Request) (*db.Catalog, catalog.Catalog, *catalog.Template) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetCatalog(s.db, id)
	if row == nil {
		return nil, catalog.Catalog{}, nil
	}
	c, err := catalog.Parse(row.Name, row.YAML)
	if err != nil {
		return row, catalog.Catalog{}, nil
	}
	return row, c, c.Get(r.PathValue("entry"))
}

func (s *Server) handleCatalogEntryForm(w http.ResponseWriter, r *http.Request) {
	row, c, entry := s.entryContext(r)
	if row == nil {
		redirectCatalogs(w, r, "That catalogue is gone.")
		return
	}
	if c.Name == "" && row.YAML != "" && entry == nil && r.PathValue("entry") != "new" {
		redirectCatalogs(w, r, "That catalogue cannot be read, so its entries cannot be edited in the form. Fix the document first.")
		return
	}
	s.renderEntryForm(w, r, row, entry, nil)
}

func (s *Server) renderEntryForm(w http.ResponseWriter, r *http.Request, row *db.Catalog, entry *catalog.Template, errs []error) {
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	title := "New entry"
	if entry != nil && entry.Name != "" {
		title = entry.Name
	}
	s.render(w, r, "catalog_entry", map[string]any{
		"Title":      title,
		"Catalog":    row,
		"Entry":      entry,
		"IsNew":      r.PathValue("entry") == "new",
		"Errors":     msgs,
		"Categories": s.catalog().Categories,
		// The empty row the page clones when a choice is added.
		"Blank": catalog.Param{},
	})
}

// readEntryForm builds an entry from the form. The parameter rows arrive as
// parallel lists — the browser sends one value per field per row, in order —
// so they are zipped back together here.
func readEntryForm(r *http.Request) *catalog.Template {
	field := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	port, _ := strconv.Atoi(field("port"))

	e := &catalog.Template{
		ID:             strings.ToLower(field("id")),
		Name:           field("name"),
		Description:    field("description"),
		Category:       field("category"),
		AppName:        field("app_name"),
		Subdomain:      field("subdomain"),
		DeployType:     field("deploy_type"),
		ImageRef:       field("image_ref"),
		DataMount:      field("data_mount"),
		Compose:        strings.ReplaceAll(r.FormValue("compose"), "\r\n", "\n"),
		ComposeService: field("compose_service"),
		Port:           port,
		Env:            strings.ReplaceAll(r.FormValue("env"), "\r\n", "\n"),
		Raw:            r.FormValue("raw") != "",
		Note:           field("note"),
	}
	// An image entry carrying a compose file is rejected by the checks, and an
	// operator who switched the type after typing one should not have to empty
	// the box to get past that.
	if e.Type() == "image" {
		e.Compose, e.ComposeService = "", ""
	} else {
		e.ImageRef = ""
	}

	names := r.Form["param_name"]
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue // a row left blank is a row not filled in, not an error
		}
		p := catalog.Param{
			Name:    name,
			Label:   strings.TrimSpace(at(r.Form["param_label"], i)),
			Kind:    at(r.Form["param_kind"], i),
			Default: strings.TrimSpace(at(r.Form["param_default"], i)),
			Help:    strings.TrimSpace(at(r.Form["param_help"], i)),
		}
		for _, opt := range strings.Split(at(r.Form["param_options"], i), ",") {
			if opt = strings.TrimSpace(opt); opt != "" {
				p.Options = append(p.Options, opt)
			}
		}
		e.Params = append(e.Params, p)
	}
	return e
}

func at(list []string, i int) string {
	if i < len(list) {
		return list[i]
	}
	return ""
}

func (s *Server) handleCatalogEntrySave(w http.ResponseWriter, r *http.Request) {
	row, c, existing := s.entryContext(r)
	if row == nil {
		redirectCatalogs(w, r, "That catalogue is gone.")
		return
	}
	entry := readEntryForm(r)
	if entry.ID == "" {
		s.renderEntryForm(w, r, row, entry, []error{formError("An entry needs an id. It is proposed as the subdomain, so keep it to lowercase letters, digits and hyphens.")})
		return
	}

	// Splice the entry into the document: in place when it is one the catalogue
	// already holds, at the end when it is new. Editing the id of an existing
	// entry moves it rather than leaving the old one behind.
	next := c
	next.Templates = slices.Clone(c.Templates)
	switch {
	case existing != nil:
		i := slices.IndexFunc(next.Templates, func(t catalog.Template) bool { return t.ID == existing.ID })
		next.Templates[i] = *entry
	default:
		if next.Get(entry.ID) != nil {
			s.renderEntryForm(w, r, row, entry, []error{formError("This catalogue already has an entry with that id.")})
			return
		}
		next.Templates = append(next.Templates, *entry)
	}
	// A category the entry invents has to be declared, or Grouped drops the
	// entry and it never appears on the page.
	if entry.Category != "" && !slices.Contains(next.Categories, entry.Category) &&
		!slices.Contains(catalog.Builtin().Categories, entry.Category) {
		next.Categories = append(next.Categories, entry.Category)
	}

	if errs := next.Validate(); len(errs) > 0 {
		s.renderEntryForm(w, r, row, entry, errs)
		return
	}
	doc, err := next.YAML()
	if err != nil {
		s.renderEntryForm(w, r, row, entry, []error{err})
		return
	}
	row.YAML = doc
	if err := db.UpdateCatalog(s.db, row); err != nil {
		s.renderEntryForm(w, r, row, entry, []error{err})
		return
	}
	s.audit(r, "catalog.entry-save", row.Name, entry.ID)
	redirectCatalogs(w, r, "Entry “"+entry.Name+"” saved in “"+row.Name+"”.")
}

func (s *Server) handleCatalogEntryDelete(w http.ResponseWriter, r *http.Request) {
	row, c, entry := s.entryContext(r)
	if row == nil || entry == nil {
		redirectCatalogs(w, r, "That entry is gone.")
		return
	}
	next := c
	next.Templates = slices.DeleteFunc(slices.Clone(c.Templates),
		func(t catalog.Template) bool { return t.ID == entry.ID })

	doc, err := next.YAML()
	if err != nil {
		redirectCatalogs(w, r, "Could not remove that: "+err.Error())
		return
	}
	row.YAML = doc
	if err := db.UpdateCatalog(s.db, row); err != nil {
		redirectCatalogs(w, r, "Could not remove that: "+err.Error())
		return
	}
	s.audit(r, "catalog.entry-delete", row.Name, entry.ID)
	redirectCatalogs(w, r, "Entry “"+entry.Name+"” removed from “"+row.Name+"”.")
}
