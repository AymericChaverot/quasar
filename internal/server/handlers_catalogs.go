package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// CatalogView is one stored catalogue as the page shows it: the row, plus what
// reading it says about it. A catalogue that no longer parses is still listed —
// this page is the only place that would ever say so.
type CatalogView struct {
	*db.Catalog
	Entries    []catalog.Template
	Categories []string
	Problems   []string
}

// Overrides names the built-in entries this catalogue replaces, which is worth
// saying out loud: an operator who reused an id by accident sees their entry
// where a familiar one used to be and has no other way to find out why.
func (v CatalogView) Overrides() []string {
	var out []string
	for _, e := range v.Entries {
		if b := catalog.Builtin().Get(e.ID); b != nil {
			out = append(out, b.Name)
		}
	}
	return out
}

func (s *Server) catalogViews() []CatalogView {
	rows, err := db.ListCatalogs(s.db)
	if err != nil {
		log.Printf("catalog: listing: %v", err)
		return nil
	}
	out := make([]CatalogView, 0, len(rows))
	for _, row := range rows {
		v := CatalogView{Catalog: row}
		c, err := catalog.Parse(row.Name, row.YAML)
		if err != nil {
			v.Problems = []string{err.Error()}
		} else {
			v.Entries, v.Categories = c.Templates, c.Categories
			for _, e := range c.Validate() {
				v.Problems = append(v.Problems, e.Error())
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) catalogsData(r *http.Request) map[string]any {
	return map[string]any{
		"Title":     "Catalogues",
		"Catalogs":  s.catalogViews(),
		"Example":   catalog.Example,
		"Saved":     r.URL.Query().Get("msg"),
		"BuiltinN":  len(catalog.Builtin().Templates),
		"CatalogsN": len(s.catalog().Templates),
	}
}

func (s *Server) handleCatalogs(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "catalogs", s.catalogsData(r))
}

func redirectCatalogs(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/settings/catalogs?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// renderCatalogsError re-renders the page with what went wrong and what was
// typed. A rejected document is not thrown away: the errors are a list of
// things to fix in text the operator may have spent a while writing, so both
// have to survive the round trip.
func (s *Server) renderCatalogsError(w http.ResponseWriter, r *http.Request, form catalogForm, errs []error) {
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	data := s.catalogsData(r)
	data["Errors"] = msgs
	data["Draft"] = form
	s.render(w, r, "catalogs", data)
}

// catalogForm is a submitted catalogue, kept whole so a rejected one can be
// handed back to the form it came from.
type catalogForm struct {
	ID        int64
	Name      string
	SourceURL string
	YAML      string
}

func readCatalogForm(r *http.Request) catalogForm {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return catalogForm{
		ID:        id,
		Name:      strings.TrimSpace(r.FormValue("name")),
		SourceURL: strings.TrimSpace(r.FormValue("source_url")),
		YAML:      strings.ReplaceAll(r.FormValue("yaml"), "\r\n", "\n"),
	}
}

// checkCatalog parses and validates a submitted document, returning it along
// with everything wrong with it. Saving one that does not parse would take the
// entries it holds out of the page without ever saying so.
//
// The catalogue it returns is what gets stored, name included: a document that
// names itself wins over the name typed beside it, because that name is what
// every entry is labelled with and two of them would be one too many.
func checkCatalog(f catalogForm) (catalog.Catalog, []error) {
	if f.Name == "" {
		return catalog.Catalog{}, []error{formError("A catalogue needs a name; it is what its entries are labelled with.")}
	}
	if strings.TrimSpace(f.YAML) == "" {
		return catalog.Catalog{}, []error{formError("There is nothing in the document.")}
	}
	c, err := catalog.Parse(f.Name, f.YAML)
	if err != nil {
		return catalog.Catalog{}, []error{err}
	}
	if len(c.Templates) == 0 {
		return c, []error{formError("The document declares no entries.")}
	}
	return c, c.Validate()
}

func (s *Server) handleCatalogCreate(w http.ResponseWriter, r *http.Request) {
	f := readCatalogForm(r)
	c, errs := checkCatalog(f)
	if len(errs) > 0 {
		s.renderCatalogsError(w, r, f, errs)
		return
	}
	row := &db.Catalog{Name: c.Name, SourceURL: f.SourceURL, YAML: f.YAML, Enabled: true}
	if _, err := db.InsertCatalog(s.db, row); err != nil {
		s.renderCatalogsError(w, r, f, []error{err})
		return
	}
	s.audit(r, "catalog.create", c.Name, fmt.Sprintf("%d entries", len(c.Templates)))
	redirectCatalogs(w, r, "Catalogue “"+c.Name+"” added.")
}

func (s *Server) handleCatalogUpdate(w http.ResponseWriter, r *http.Request) {
	f := readCatalogForm(r)
	row := db.GetCatalog(s.db, f.ID)
	if row == nil {
		redirectCatalogs(w, r, "That catalogue is gone.")
		return
	}
	c, errs := checkCatalog(f)
	if len(errs) > 0 {
		s.renderCatalogsError(w, r, f, errs)
		return
	}
	row.Name, row.SourceURL, row.YAML = c.Name, f.SourceURL, f.YAML
	if err := db.UpdateCatalog(s.db, row); err != nil {
		s.renderCatalogsError(w, r, f, []error{err})
		return
	}
	s.audit(r, "catalog.update", c.Name, fmt.Sprintf("%d entries", len(c.Templates)))
	redirectCatalogs(w, r, "Catalogue “"+c.Name+"” saved.")
}

func (s *Server) handleCatalogToggle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetCatalog(s.db, id)
	if row == nil {
		redirectCatalogs(w, r, "That catalogue is gone.")
		return
	}
	on := !row.Enabled
	if err := db.SetCatalogEnabled(s.db, id, on); err != nil {
		redirectCatalogs(w, r, "Could not change that: "+err.Error())
		return
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	s.audit(r, "catalog.toggle", row.Name, state)
	redirectCatalogs(w, r, "Catalogue “"+row.Name+"” "+state+".")
}

func (s *Server) handleCatalogDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetCatalog(s.db, id)
	if row == nil {
		redirectCatalogs(w, r, "That catalogue is gone.")
		return
	}
	if err := db.DeleteCatalog(s.db, id); err != nil {
		redirectCatalogs(w, r, "Could not delete that: "+err.Error())
		return
	}
	s.audit(r, "catalog.delete", row.Name, "")
	// Deleting a catalogue takes its entries off the page and leaves every
	// application deployed from one exactly where it is: an entry is a form
	// prefill, and nothing reads it again after the app is created.
	redirectCatalogs(w, r, "Catalogue “"+row.Name+"” deleted. Applications deployed from it are unaffected.")
}

// handleCatalogFetch imports a catalogue from a URL, or re-fetches one already
// imported. Nothing here happens on a timer: a catalogue is the compose files
// this server will run, and one that quietly rewrote itself between a look and
// a deploy would be a bad thing to have built.
func (s *Server) handleCatalogFetch(w http.ResponseWriter, r *http.Request) {
	f := readCatalogForm(r)
	var row *db.Catalog
	if f.ID != 0 {
		if row = db.GetCatalog(s.db, f.ID); row == nil {
			redirectCatalogs(w, r, "That catalogue is gone.")
			return
		}
		f.SourceURL, f.Name = row.SourceURL, row.Name
	}

	doc, err := fetchCatalog(f.SourceURL)
	if err != nil {
		s.renderCatalogsError(w, r, f, []error{err})
		return
	}
	f.YAML = doc
	if f.Name == "" {
		f.Name = nameFromURL(f.SourceURL)
	}
	c, errs := checkCatalog(f)
	if len(errs) > 0 {
		s.renderCatalogsError(w, r, f, errs)
		return
	}

	if row == nil {
		row = &db.Catalog{Name: c.Name, SourceURL: f.SourceURL, YAML: f.YAML, Enabled: true}
		if _, err := db.InsertCatalog(s.db, row); err != nil {
			s.renderCatalogsError(w, r, f, []error{err})
			return
		}
		s.audit(r, "catalog.import", c.Name, f.SourceURL)
		redirectCatalogs(w, r, "Imported “"+c.Name+"” from "+f.SourceURL+".")
		return
	}
	row.Name, row.YAML = c.Name, f.YAML
	if err := db.UpdateCatalog(s.db, row); err != nil {
		s.renderCatalogsError(w, r, f, []error{err})
		return
	}
	s.audit(r, "catalog.refresh", c.Name, f.SourceURL)
	redirectCatalogs(w, r, "Refreshed “"+c.Name+"” from "+f.SourceURL+".")
}

// maxCatalogBytes caps what a fetch will read. A catalogue is a page or two of
// YAML; anything approaching this is not one, and reading it into memory to
// find that out is the failure mode worth closing.
const maxCatalogBytes = 1 << 20

func fetchCatalog(raw string) (string, error) {
	return fetchDocument(raw, "catalogue", maxCatalogBytes)
}

// fetchDocument reads a YAML document an operator asked for by address, once,
// now. What it is called is only for the message a document too large comes
// back with, which is the one place the difference between a catalogue and a
// station is worth stating.
func fetchDocument(raw, kind string, max int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", formError("That is not an http or https URL.")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return "", fmt.Errorf("could not fetch %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", u, err)
	}
	if len(body) > max {
		return "", fmt.Errorf("%s is larger than a %s has any business being", u, kind)
	}
	return strings.ReplaceAll(string(body), "\r\n", "\n"), nil
}

// nameFromURL is the fallback name for an imported catalogue that does not name
// itself: the file it came from, without its extension.
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "Imported catalogue"
	}
	base := u.Path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	if base == "" {
		return "Imported catalogue"
	}
	return base
}
