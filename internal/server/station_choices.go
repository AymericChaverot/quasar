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
	at      time.Time
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

func (c *stationChoiceCache) put(stationID, action string, options []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[string]stationChoice{}
	}
	c.by[c.key(stationID, action)] = stationChoice{options: options, at: time.Now()}
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
		params[i].Options = merge(p.Options, s.stationChoices(ctx, r, st, p.OptionsFrom))
	}
	t.Params = params
	return t
}

// merge adds what the script offered to what the document wrote, in that
// order and without duplicates: the document's own values first, because those
// are the ones its author thought were worth having at the top of a list.
func merge(written, fetched []string) []string {
	out := slices.Clone(written)
	for _, v := range fetched {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// stationChoices is one parameter's fetched options: the cached answer while it
// is fresh, otherwise a new ask — and, when that fails, whatever was cached
// however old, or nothing at all.
func (s *Server) stationChoices(ctx context.Context, r *http.Request, st station.Station, action string) []string {
	held, known := s.choices.get(st.ID, action)
	if known && time.Since(held.at) < stationChoicesTTL {
		return held.options
	}

	options, err := s.askStationChoices(ctx, r, st, action)
	if err != nil {
		// Not an error on the page. Nothing the operator did caused it and
		// there is nothing they can do about it; what they get is the list the
		// document wrote, which is why a document that uses this still writes
		// one. The author is the one who needs to know, and the log is where
		// they look.
		log.Printf("station %s: %s could not fill in the options: %v", st.ID, action, err)
		return held.options
	}
	s.choices.put(st.ID, action, options)
	return options
}

// askStationChoices runs one options action and reads the list out of it.
func (s *Server) askStationChoices(ctx context.Context, r *http.Request,
	st station.Station, action string) ([]string, error) {

	if !slices.Contains(st.ChoiceActions(), action) {
		return nil, errors.New("no parameter of this station takes its options from " + action)
	}
	sp, err := worker.Self()
	if err != nil {
		return nil, err
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
		return nil, errors.New(stationProblem(action, err))
	}

	result := ui.ParseResult(out.Value)
	if result.Error != "" {
		return nil, errors.New(result.Error)
	}
	return optionList(result.Data)
}

// optionList reads what an options action returned: a list of values, as
// strings or as numbers, since "1.21" written in a script is a string and 20 is
// not.
func optionList(data json.RawMessage) ([]string, error) {
	if len(data) == 0 {
		return nil, errors.New("it returned nothing; an options action returns a list of values")
	}
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("it did not return a list of values")
	}

	out := make([]string, 0, len(raw))
	for _, v := range raw {
		switch value := v.(type) {
		case string:
			out = append(out, value)
		case float64:
			out = append(out, strconv.FormatFloat(value, 'f', -1, 64))
		default:
			return nil, errors.New("one of the values it returned is not a value a form could offer")
		}
	}
	return out, nil
}
