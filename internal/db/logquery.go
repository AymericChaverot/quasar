package db

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// LogQuery is what the Logs page asks of the stored lines.
type LogQuery struct {
	// App is one application's id, "" for every one.
	App string
	// Text is the search as typed: words that must all be there, "a phrase",
	// -a word that must not, and OR between alternatives. Empty matches every
	// line.
	Text string
	// Exact reads Text as a piece of text to find anywhere in a line, as it is
	// written, rather than as words.
	Exact bool
	// Since leaves out what was written before it; zero for every line.
	Since time.Time
	// Errors keeps only the lines that report a failure, by the words such a
	// line carries.
	Errors bool
}

// errorWords are what a line reporting a failure says, in the log formats
// containers write. They are prefixes: "error" finds ERROR, errors and
// error:, "panic" finds panicked.
//
// Whole words rather than shorter stems like "err": until the index is built
// they are looked for anywhere in a line, and "err" is in every "server".
var errorWords = []string{"error", "exception", "fatal", "panic", "critical", "traceback", "failed", "failure"}

// logTerm is one thing a line must, or must not, hold.
type logTerm struct {
	text   string
	phrase bool // written in quotes: the words together, in that order
}

// parsedLogQuery is a search broken into what each line must hold: any one of
// the any groups, each group's terms all together, and none of none.
type parsedLogQuery struct {
	any  [][]logTerm
	none []logTerm
}

// parseLogQuery reads a search as typed. It is forgiving rather than strict,
// as a search box has to be: a quote left open runs to the end, a dash before
// anything but a word or a quote is text, and an OR with nothing on one side is dropped.
func parseLogQuery(s string) parsedLogQuery {
	var q parsedLogQuery
	group := []logTerm{}
	flush := func() {
		if len(group) > 0 {
			q.any = append(q.any, group)
			group = []logTerm{}
		}
	}
	r := []rune(s)
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		neg := false
		// A dash in front of a word or a quote leaves it out; anywhere else it
		// is the text itself, as in "->" or "--force".
		if r[i] == '-' && i+1 < len(r) && (r[i+1] == '"' || unicode.IsLetter(r[i+1]) || unicode.IsNumber(r[i+1])) {
			neg = true
			i++
		}
		var t logTerm
		if r[i] == '"' {
			j := i + 1
			for j < len(r) && r[j] != '"' {
				j++
			}
			t = logTerm{text: strings.TrimSpace(string(r[i+1 : j])), phrase: true}
			i = j + 1
		} else {
			j := i
			for j < len(r) && !unicode.IsSpace(r[j]) {
				j++
			}
			t = logTerm{text: string(r[i:j])}
			i = j
		}
		if t.text == "" {
			continue
		}
		switch {
		case neg:
			q.none = append(q.none, t)
		case t.text == "OR" && !t.phrase:
			flush()
		default:
			group = append(group, t)
		}
	}
	flush()
	return q
}

// Highlights are the pieces of text a matching line was found by, for the
// page to mark: every term a line must hold, as written.
func (q LogQuery) Highlights() []string {
	if strings.TrimSpace(q.Text) == "" {
		return nil
	}
	if q.Exact {
		return []string{q.Text}
	}
	var out []string
	for _, g := range parseLogQuery(q.Text).any {
		for _, t := range g {
			out = append(out, t.text)
		}
	}
	return out
}

// hasWord is a term the index can look for: one with a letter or a digit in
// it. The index holds words only, so "->" or "==" is found by reading the
// lines instead.
func (t logTerm) hasWord() bool {
	return strings.IndexFunc(t.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// match is the term in the index's own language: a phrase as written, and a
// word as the start of one, so "refus" finds "refused". The index splits a
// term on its punctuation the same way it split the lines, which makes
// "user_id=42" the three words user, id, 42 side by side.
func (t logTerm) match() string {
	m := `"` + strings.ReplaceAll(t.text, `"`, `""`) + `"`
	if !t.phrase {
		m += "*"
	}
	return m
}

// like is the term as a LIKE condition, for when the index cannot answer.
func (t logTerm) like(not bool) (string, []any) {
	op := "LIKE"
	if not {
		op = "NOT LIKE"
	}
	return "line " + op + ` ? ESCAPE '\'`, []any{containsPattern(t.text)}
}

// where is the WHERE clause for q over app_logs, and nothing at all for an
// empty query, so that a page with no filter reads the ts index alone.
//
// Each filter is written into the query only when it is given: a condition
// that holds when its parameter is empty is one SQLite cannot answer from an
// index. indexed says whether the search index is complete; until it is,
// words are found by reading the lines, as they always were.
func (q LogQuery) where(indexed bool) (string, []any) {
	var conds []string
	var args []any
	add := func(c string, a ...any) {
		conds = append(conds, c)
		args = append(args, a...)
	}
	if q.App != "" {
		add("app_id = ?", q.App)
	}
	if !q.Since.IsZero() {
		add("ts >= ?", q.Since.UTC())
	}
	if q.Errors {
		if indexed {
			var m []string
			for _, w := range errorWords {
				m = append(m, logTerm{text: w}.match())
			}
			add("rowid IN (SELECT rowid FROM app_logs_fts WHERE app_logs_fts MATCH ?)", strings.Join(m, " OR "))
		} else {
			var ors []string
			for _, w := range errorWords {
				c, a := logTerm{text: w}.like(false)
				ors = append(ors, c)
				args = append(args, a...)
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
		}
	}
	text := strings.TrimSpace(q.Text)
	switch {
	case text == "":
	case q.Exact:
		add(`line LIKE ? ESCAPE '\'`, containsPattern(q.Text))
	default:
		c, a := parseLogQuery(text).where(indexed)
		if c != "" {
			add(c, a...)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// where is the condition for a parsed search. What the index can answer goes
// to it in one MATCH; a term it cannot — punctuation alone — is read from the
// lines beside it.
func (p parsedLogQuery) where(indexed bool) (string, []any) {
	var conds []string
	var args []any

	if len(p.any) > 0 {
		var groups []string
		var gargs []any
		// The whole of the alternatives goes to the index only when every
		// term in them can: an OR is one condition, and half of it cannot be
		// read one way and half the other.
		all := indexed
		for _, g := range p.any {
			for _, t := range g {
				all = all && t.hasWord()
			}
		}
		if all {
			for _, g := range p.any {
				var ms []string
				for _, t := range g {
					ms = append(ms, t.match())
				}
				groups = append(groups, "("+strings.Join(ms, " AND ")+")")
			}
			conds = append(conds, "rowid IN (SELECT rowid FROM app_logs_fts WHERE app_logs_fts MATCH ?)")
			args = append(args, strings.Join(groups, " OR "))
		} else {
			for _, g := range p.any {
				var cs []string
				for _, t := range g {
					c, a := t.like(false)
					cs = append(cs, c)
					gargs = append(gargs, a...)
				}
				groups = append(groups, "("+strings.Join(cs, " AND ")+")")
			}
			conds = append(conds, "("+strings.Join(groups, " OR ")+")")
			args = append(args, gargs...)
		}
	}

	var indexedNone []string
	for _, t := range p.none {
		if indexed && t.hasWord() {
			indexedNone = append(indexedNone, t.match())
			continue
		}
		c, a := t.like(true)
		conds = append(conds, c)
		args = append(args, a...)
	}
	if len(indexedNone) > 0 {
		conds = append(conds, "rowid NOT IN (SELECT rowid FROM app_logs_fts WHERE app_logs_fts MATCH ?)")
		args = append(args, strings.Join(indexedNone, " OR "))
	}
	return strings.Join(conds, " AND "), args
}

// --- The index -------------------------------------------------------------------

// settingLogIndexBackfill holds, while the lines stored before the search index
// existed are still being added to it, the id of the last of them. Empty once
// they all are — or when there were none.
const settingLogIndexBackfill = "log_index_backfill"

// settingLogIndexDone is how far the backfill has got, by id.
const settingLogIndexDone = "log_index_backfill_done"

// logIndexReady is whether the search index holds every stored line.
func logIndexReady(db *sql.DB) bool {
	return GetSetting(db, settingLogIndexBackfill) == ""
}

// logsToIndex is called by Open before the schema creates the search index: on
// a database that has stored lines but no index yet, it is the id of the last
// of them, which BackfillLogIndex will add once Open has noted it. The lines
// written after it reach the index through the trigger, like every line from
// then on. Zero when there is nothing to add.
func logsToIndex(db *sql.DB) (int64, error) {
	// The index already there, or no app_logs at all — a brand new database:
	// either way, nothing to add.
	var index, logs int
	if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE name = 'app_logs_fts'), COUNT(*) FILTER (WHERE name = 'app_logs')
		FROM sqlite_master`).Scan(&index, &logs); err != nil || index > 0 || logs == 0 {
		return 0, err
	}
	var last sql.NullInt64
	err := db.QueryRow("SELECT MAX(id) FROM app_logs").Scan(&last)
	return last.Int64, err
}

// logIndexBatch is how many lines one step of the backfill adds: small enough
// that the log writer, waiting on the same lock, is never held for long.
const logIndexBatch = 2000

// BackfillLogIndex adds to the search index the lines stored before it
// existed, a batch at a time, and returns how many it added. Until it is done
// a search reads the lines themselves, as it did before there was an index.
//
// It picks up where it stopped, so a restart halfway through costs nothing. A
// line deleted meanwhile is simply not there to add; one deleted after it was
// added leaves the index through the trigger, like any other.
func BackfillLogIndex(db *sql.DB, pause time.Duration) (int64, error) {
	var added int64
	for {
		last, _ := strconv.ParseInt(GetSetting(db, settingLogIndexBackfill), 10, 64)
		if last == 0 {
			return added, nil
		}
		done, _ := strconv.ParseInt(GetSetting(db, settingLogIndexDone), 10, 64)
		tx, err := db.Begin()
		if err != nil {
			return added, err
		}
		var upto sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(id) FROM (SELECT id FROM app_logs WHERE id > ? AND id <= ? ORDER BY id LIMIT ?)`,
			done, last, logIndexBatch).Scan(&upto); err != nil {
			_ = tx.Rollback()
			return added, err
		}
		if !upto.Valid {
			// Nothing left: the index is whole.
			_, err := tx.Exec("DELETE FROM settings WHERE key IN (?, ?)", settingLogIndexBackfill, settingLogIndexDone)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			return added, err
		}
		res, err := tx.Exec(`INSERT INTO app_logs_fts(rowid, line) SELECT id, line FROM app_logs WHERE id > ? AND id <= ?`, done, upto.Int64)
		if err == nil {
			_, err = tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", settingLogIndexDone, strconv.FormatInt(upto.Int64, 10))
		}
		if err != nil {
			_ = tx.Rollback()
			return added, err
		}
		if err := tx.Commit(); err != nil {
			return added, err
		}
		n, _ := res.RowsAffected()
		added += n
		if pause > 0 {
			time.Sleep(pause)
		}
	}
}
