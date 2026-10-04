package server

import (
	"bytes"
	"html"
	"strings"
	"testing"
)

// A flash is the answer to what one tab asked, so it is drawn once, by the
// next page of that session, and by no other.
func TestAFlashIsDrawnOnceByItsOwnSession(t *testing.T) {
	var b noticeBoard
	b.addFlash("tab-a", Notice{Kind: noticeOK, Title: "Saved"})

	if got := b.take("tab-b", 1); len(got) != 0 {
		t.Errorf("another session drew it: %v", got)
	}
	if got := b.take("tab-a", 1); len(got) != 1 || got[0].Title != "Saved" {
		t.Errorf("its own session did not: %v", got)
	}
	if got := b.take("tab-a", 1); len(got) != 0 {
		t.Errorf("it came back on the next page: %v", got)
	}
}

// The end of something started elsewhere goes to every open page of the person
// who started it, and waits for the next one if none is open.
func TestAFinishedJobReachesItsOwnerWhereverTheyAre(t *testing.T) {
	var b noticeBoard
	ch := b.subscribe(7)
	b.deliver(7, Notice{Kind: noticeOK, Title: "Cleanup"})
	select {
	case n := <-ch:
		if n.Title != "Cleanup" {
			t.Errorf("got %v", n)
		}
	default:
		t.Fatal("the open page was not told")
	}
	if got := b.take("", 7); len(got) != 0 {
		t.Errorf("it was told and kept as well: %v", got)
	}

	other := b.subscribe(8)
	b.unsubscribe(7, ch)
	b.deliver(7, Notice{Kind: noticeErr, Title: "Backup"})
	select {
	case n := <-other:
		t.Errorf("somebody else was told: %v", n)
	default:
	}
	if got := b.take("", 7); len(got) != 1 || got[0].Title != "Backup" {
		t.Errorf("with no page open, it was not kept for the next one: %v", got)
	}
}

// A night of failing jobs opens on the latest few, not a column of them.
func TestHeldNoticesAreCapped(t *testing.T) {
	var b noticeBoard
	for i := range heldNotices + 3 {
		b.deliver(1, Notice{Title: string(rune('a' + i))})
	}
	got := b.take("", 1)
	if len(got) != heldNotices || got[len(got)-1].Title != string(rune('a'+heldNotices+2)) {
		t.Errorf("kept %d, last %q", len(got), got[len(got)-1].Title)
	}
}

// Each kind says which it is in a symbol as well as a colour, goes by itself
// or stays as it should, and can be dismissed.
func TestEachKindOfToastRenders(t *testing.T) {
	s := testServer(t)
	draw := func(n Notice) string {
		t.Helper()
		var buf bytes.Buffer
		if err := s.pages["dashboard"].ExecuteTemplate(&buf, "toast", n); err != nil {
			t.Fatal(err)
		}
		return html.UnescapeString(buf.String())
	}
	cases := []struct {
		kind, life, role string
	}{
		{noticeOK, `data-life="6000"`, `role="status"`},
		{noticeInfo, `data-life="6000"`, `role="status"`},
		{noticeWarn, `data-life="9000"`, `role="status"`},
		{noticeErr, "", `role="alert"`},
	}
	for _, c := range cases {
		got := draw(Notice{Kind: c.kind, Title: "Title " + c.kind, Text: "Text", Detail: "detail"})
		for _, want := range []string{"toast-" + c.kind, "Title " + c.kind, "toast-text", "toast-detail", "<svg", "Dismiss", c.role} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: no %q in\n%s", c.kind, want, got)
			}
		}
		if c.life == "" && strings.Contains(got, "data-life") {
			t.Errorf("an error takes itself off the page:\n%s", got)
		}
		if c.life != "" && !strings.Contains(got, c.life) {
			t.Errorf("%s: no %s in\n%s", c.kind, c.life, got)
		}
	}
	if got := draw(Notice{Kind: noticeInfo, Title: "Cleanup", ID: "cleanup-1"}); !strings.Contains(got, `data-notice="cleanup-1"`) {
		t.Errorf("a notice that can be replaced does not say so:\n%s", got)
	}
}

// A toast goes over the stream as one event, so it has to fit on one line.
func TestARenderedNoticeIsOneLine(t *testing.T) {
	s := testServer(t)
	got := s.renderNotice(Notice{Kind: noticeOK, Title: "Saved", Text: "Done"})
	if got == "" || strings.Contains(got, "\n") {
		t.Errorf("%q", got)
	}
}
