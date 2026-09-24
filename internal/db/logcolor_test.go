package db

import (
	"math"
	"testing"
)

// A colour placed at a hue reads back as that hue: the two conversions are
// each other's inverse, which is what lets a colour typed in by hand be kept
// away from like one Quasar chose.
func TestLogColorHueRoundTrip(t *testing.T) {
	for _, h := range []float64{0, 45, 120, 200, 250, 310} {
		got, ok := hueOf(oklchHex(logColorLightness, logColorChroma, h))
		if !ok {
			t.Fatalf("hue %v: no hue read back", h)
		}
		d := math.Abs(got - h)
		if d = math.Min(d, 360-d); d > 3 {
			t.Errorf("hue %v reads back as %.1f", h, got)
		}
	}
	if _, ok := hueOf("#808080"); ok {
		t.Error("a grey has a hue to keep away from")
	}
}

// Each new colour goes as far as it can from those already taken: the second
// opposite the first, the third and fourth into the gaps left.
func TestNextLogColorSpreadsTheHues(t *testing.T) {
	var taken []string
	var hues []float64
	for range 4 {
		c := nextLogColor(taken)
		h, _ := hueOf(c)
		taken, hues = append(taken, c), append(hues, h)
	}
	for i := range hues {
		for j := range i {
			d := math.Abs(hues[i] - hues[j])
			if d = math.Min(d, 360-d); d < 85 {
				t.Errorf("hues %.0f and %.0f are only %.0f° apart in %v", hues[i], hues[j], d, taken)
			}
		}
	}
}

// Applications are coloured as they are inserted, and the ones that predate
// colours are given theirs in the order they were created, without touching a
// colour somebody chose.
func TestAppsGetDistinctLogColors(t *testing.T) {
	database := openTestDB(t)
	k := testKeyring(t)
	for _, id := range []string{"a", "b"} {
		if err := InsertApp(database, k, &App{ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx"}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := GetApp(database, k, "a")
	b, _ := GetApp(database, k, "b")
	if !IsHexColor(a.LogColor) || !IsHexColor(b.LogColor) || a.LogColor == b.LogColor {
		t.Fatalf("colours %q and %q, want two different ones", a.LogColor, b.LogColor)
	}

	if err := UpdateAppLogColor(database, "a", "#FF0000"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAppLogColor(database, "a", "red"); err == nil {
		t.Error("a colour that is not #rrggbb is stored")
	}
	database.Exec("UPDATE apps SET log_color = '' WHERE id = 'b'")
	if err := AssignLogColors(database); err != nil {
		t.Fatal(err)
	}
	a, _ = GetApp(database, k, "a")
	b, _ = GetApp(database, k, "b")
	if a.LogColor != "#ff0000" {
		t.Errorf("the chosen colour became %q", a.LogColor)
	}
	if h, _ := hueOf(b.LogColor); math.Min(math.Abs(h-29), 360-math.Abs(h-29)) < 150 {
		t.Errorf("the backfilled colour %q (hue %.0f) is not across from red", b.LogColor, h)
	}
}
