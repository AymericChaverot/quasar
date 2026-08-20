package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"quasar/internal/catalog"
	"quasar/internal/station"
	"quasar/internal/station/ui"
	"quasar/internal/station/worker"
)

// The choices a station's install form offers.
//
// A parameter is normally a list somebody typed into the document, which is
// right for the ones that are short and stay still — a mod loader, a
// difficulty. It is wrong for the ones that are neither. Every release of
// Minecraft there has ever been is a list that grows without the station being
// touched, so a document that writes it out is a document that is out of date
// by the next release, and the alternative it was written as instead — a free
// text box — accepts "1.21.9" happily and hands the operator a container that
// will not start, with nothing on the page to say why.
//
// So a parameter may name an action of the station's own, and the station is
// asked. This is the only time a station's script runs with no application: it
// has nothing to act on and reaches nothing but the hosts its net.external
// permission names, which is checked here exactly as it is for every other
// call.
//
// What arrives is added to the options the document wrote; it never replaces
// them. The written ones are the answer when the answer does not arrive, and a
// form that empties itself because somebody else's API is having a bad
// afternoon is worse than a short one.

const (
	// stationChoicesTTL is how long an answer stands. These lists change when
	// somebody releases something, which is not on the hour, and asking again
	// on every draw of a form would mean three requests to somebody else's API
	// per operator filling one in.
	stationChoicesTTL = time.Hour

	// stationChoicesBudget bounds the whole ask. Somebody is waiting on a page
	// while it runs, and a station whose action sits on a socket for thirty
	// seconds must not be able to hold the install form shut: the answer is
	// late, the form draws with what the document wrote.
	stationChoicesBudget = 8 * time.Second
)

// CallChoices is the budget kind for one of these: a page load, like a panel
// source, and held to the same ceiling.
const CallChoices = CallSource

// stationChoice is one answer, and when it arrived.
type stationChoice struct {
	options []string

	// picked is the one to start on, when the action named one. It is how a
	// form defaults to the newest release rather than to whatever the document
	// was written around — and it comes from the action rather than from the
	// order of the list, because "the newest" is something the source says and
	// not something a position in an array means.
	picked string

	at time.Time
}

// stationChoiceCache holds the last answer per station and action.
//
// In memory, like the job registry beside it: this is a cache of somebody
// else's list, it costs one request to rebuild, and a dashboard that restarted
// should ask again rather than serve what it remembered from last week.
type stationChoiceCache struct {
	mu sync.Mutex
	by map[string]stationChoice
}

func (c *stationChoiceCache) key(stationID, action string) string {
	return stationID + "\x00" + action
}

// get returns the last answer and whether it is still fresh. A stale one is
// still worth having: it is a real list, and the alternative when the next ask
// fails is the handful of values the document wrote.
func (c *stationChoiceCache) get(stationID, action string) (stationChoice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, ok := c.by[c.key(stationID, action)]
	return held, ok
}

func (c *stationChoiceCache) put(stationID, action string, answer stationChoice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[string]stationChoice{}
	}
	answer.at = time.Now()
	c.by[c.key(stationID, action)] = answer
}

// stationTemplate is a station's deploy block as the form and the deployment
// should see it: the catalogue entry it is, with every parameter that takes
// its options from the script carrying what the script said.
//
// Both paths go through here, and that is the point. The form offers a list
// and the deployment accepts a value; if only the first knew about the fetched
// options, every dynamic choice would be silently replaced by the default at
// the moment somebody pressed the button.
func (s *Server) stationTemplate(ctx context.Context, r *http.Request, st station.Station) catalog.Template {
	t := st.Template()
	if len(st.ChoiceActions()) == 0 {
		return t
	}

	params := slices.Clone(t.Params)
	for i, p := range params {
		if p.OptionsFrom == "" {
			continue
		}
		answer := s.stationChoices(ctx, r, st, p.OptionsFrom)
		params[i].Options = merge(answer.options, p.Options)

		// The action's own default, where it named one it is also offering. A
		// form that proposed a value it would then refuse would be worse than
		// one that proposed the document's, which is what stands otherwise —
		// and what stands when nothing came back at all.
		if slices.Contains(params[i].Options, answer.picked) {
			params[i].Default = answer.picked
		}
	}
	t.Params = params
	return t
}

// merge is the offered list, followed by anything the document wrote that the
// answer did not mention.
//
// This way round because an answer is the source speaking: it knows what
// exists and in what order — newest first, for a list of releases — while the
// written list is the offline fallback and a place for values the source has no
// concept of. Ordering the document's first would put a version somebody typed
// out a year ago above the one that came out on Tuesday.
func merge(offered, written []string) []string {
	out := make([]string, 0, len(offered)+len(written))
	for _, v := range slices.Concat(offered, written) {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// stationChoices is one parameter's fetched answer: the cached one while it is
// fresh, otherwise a new ask — and, when that fails, whatever was cached
// however old, or nothing at all.
func (s *Server) stationChoices(ctx context.Context, r *http.Request, st station.Station, action string) stationChoice {
	held, known := s.choices.get(st.ID, action)
	if known && time.Since(held.at) < stationChoicesTTL {
		return held
	}

	answer, err := s.askStationChoices(ctx, r, st, action)
	if err != nil {
		// Not an error on the page. Nothing the operator did caused it and
		// there is nothing they can do about it; what they get is the list the
		// document wrote, which is why a document that uses this still writes
		// one. The author is the one who needs to know, and the log is where
		// they look.
		log.Printf("station %s: %s could not fill in the options: %v", st.ID, action, err)
		return held
	}
	s.choices.put(st.ID, action, answer)
	return answer
}

// askStationChoices runs one options action and reads the answer out of it.
func (s *Server) askStationChoices(ctx context.Context, r *http.Request,
	st station.Station, action string) (stationChoice, error) {

	if !slices.Contains(st.ChoiceActions(), action) {
		return stationChoice{}, errors.New("no parameter of this station takes its options from " + action)
	}
	sp, err := worker.Self()
	if err != nil {
		return stationChoice{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, stationChoicesBudget)
	defer cancel()

	// No application: App is left empty, so quasar.app is an object with
	// nothing in it and the broker refuses every capability that would need
	// one. No input either — the form is drawn before anything has been
	// answered, so there are no answers to hand over.
	call := worker.Call{Script: st.Script, Action: action, Input: json.RawMessage(`{}`)}
	out, err := worker.Run(ctx, sp, call, stationLimits(CallChoices), &stationCall{srv: s, doc: st, r: r})
	if err != nil {
		return stationChoice{}, errors.New(stationProblem(action, err))
	}

	result := ui.ParseResult(out.Value)
	if result.Error != "" {
		return stationChoice{}, errors.New(result.Error)
	}
	return optionList(result.Data)
}

// optionList reads what an options action returned.
//
// A list of values is the whole of it in the ordinary case. The other shape —
// `{options: [...], default: 'x'}` — is for the source that also knows which
// one to start on: Mojang publishes the newest release beside the list of every
// release, and a form defaulting to whatever version the document was written
// around is a form that proposes last year's server to somebody who wanted a
// new one.
//
// Both are accepted because both are the same intent written at different
// lengths, which is the rule the rest of the format already follows: a stat
// takes a number or an object with one in it.
func optionList(data json.RawMessage) (stationChoice, error) {
	if len(data) == 0 {
		return stationChoice{}, errors.New("it returned nothing; an options action returns a list of values")
	}

	if bare, err := values(data); err == nil {
		return stationChoice{options: bare}, nil
	}
	var full struct {
		Options json.RawMessage `json:"options"`
		Default any             `json:"default"`
	}
	if err := json.Unmarshal(data, &full); err != nil || len(full.Options) == 0 {
		return stationChoice{}, errors.New("it did not return a list of values, or an object with options in it")
	}
	options, err := values(full.Options)
	if err != nil {
		return stationChoice{}, err
	}
	return stationChoice{options: options, picked: value(full.Default)}, nil
}

// values reads a list of values, as strings or as numbers, since "1.21"
// written in a script is a string and 20 is not.
func values(data json.RawMessage) ([]string, error) {
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("it did not return a list of values")
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s := value(v); s != "" {
			out = append(out, s)
			continue
		}
		return nil, errors.New("one of the values it returned is not a value a form could offer")
	}
	return out, nil
}

// value is one option as a form would carry it, empty for anything a form
// could not.
func value(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}
