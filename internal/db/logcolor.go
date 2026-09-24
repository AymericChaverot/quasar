package db

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// An application's colour on the Logs page, where lines from every application
// are interleaved and the name column is what tells them apart at a glance. It
// is stored rather than worked out from the name, so it never changes on its
// own, and each new application takes the hue furthest from the ones already
// taken, so no two end up near each other while there is room.
//
// Hues are placed in OKLCH rather than HSL: at one lightness and chroma, every
// hue there reads as equally bright, where HSL's yellow glares next to its
// blue.

const (
	logColorLightness = 0.72
	logColorChroma    = 0.14
	// firstLogHue is where the first application goes: a blue, which reads as
	// nothing in particular — not an error, not a warning.
	firstLogHue = 250
)

// nextLogColor is the colour for an application joining ones coloured taken.
func nextLogColor(taken []string) string {
	var hues []float64
	for _, c := range taken {
		if h, ok := hueOf(c); ok {
			hues = append(hues, h)
		}
	}
	return oklchHex(logColorLightness, logColorChroma, farthestHue(hues))
}

// farthestHue is the whole-degree hue furthest round the circle from every
// one in hues; the lowest such hue when several tie.
func farthestHue(hues []float64) float64 {
	if len(hues) == 0 {
		return firstLogHue
	}
	best, bestGap := 0.0, -1.0
	for h := 0.0; h < 360; h++ {
		gap := 360.0
		for _, t := range hues {
			d := math.Abs(h - t)
			gap = math.Min(gap, math.Min(d, 360-d))
		}
		if gap > bestGap {
			best, bestGap = h, gap
		}
	}
	return best
}

// AssignLogColors gives every application that has no colour yet one, in the
// order they were created, so an existing install comes out the same as if
// each had been coloured on the day it was added.
func AssignLogColors(db *sql.DB) error {
	ids, taken, err := appsByLogColor(db)
	if err != nil {
		return err
	}
	for _, id := range ids {
		color := nextLogColor(taken)
		if _, err := db.Exec("UPDATE apps SET log_color = ? WHERE id = ?", color, id); err != nil {
			return err
		}
		taken = append(taken, color)
	}
	return nil
}

// appsByLogColor splits the applications, oldest first, into those with no
// colour yet and the colours the others already have. The rows are read to
// the end before anything is written: the pool is a single connection.
func appsByLogColor(db *sql.DB) (uncolored, taken []string, err error) {
	rows, err := db.Query("SELECT id, log_color FROM apps ORDER BY created_at, rowid")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, color string
		if err := rows.Scan(&id, &color); err != nil {
			return nil, nil, err
		}
		if color == "" {
			uncolored = append(uncolored, id)
		} else {
			taken = append(taken, color)
		}
	}
	return uncolored, taken, rows.Err()
}

// takenLogColors is every colour already given to an application.
func takenLogColors(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT log_color FROM apps WHERE log_color != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateAppLogColor sets the colour an application is drawn in on the Logs
// page. It must be a #rrggbb colour.
func UpdateAppLogColor(db *sql.DB, id, color string) error {
	if !IsHexColor(color) {
		return fmt.Errorf("%q is not a #rrggbb colour", color)
	}
	_, err := db.Exec("UPDATE apps SET log_color = ? WHERE id = ?", strings.ToLower(color), id)
	return err
}

// IsHexColor reports whether s is a #rrggbb colour, the form a colour input
// submits.
func IsHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 16, 32)
	return err == nil
}

// hueOf is a colour's OKLCH hue in degrees. A grey has none worth keeping
// away from, and says so.
func hueOf(hex string) (float64, bool) {
	if !IsHexColor(hex) {
		return 0, false
	}
	v, _ := strconv.ParseUint(hex[1:], 16, 32)
	r, g, b := float64(v>>16&0xff)/255, float64(v>>8&0xff)/255, float64(v&0xff)/255
	r, g, b = toLinear(r), toLinear(g), toLinear(b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	A := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	if math.Hypot(A, B) < 0.03 {
		return 0, false
	}
	h := math.Atan2(B, A) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return h, true
}

// oklchHex is the sRGB colour nearest to an OKLCH one, as #rrggbb.
func oklchHex(L, C, h float64) string {
	A, B := C*math.Cos(h*math.Pi/180), C*math.Sin(h*math.Pi/180)
	l := cube(L + 0.3963377774*A + 0.2158037573*B)
	m := cube(L - 0.1055613458*A - 0.0638541728*B)
	s := cube(L - 0.0894841775*A - 1.2914855480*B)
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return fmt.Sprintf("#%02x%02x%02x", toByte(r), toByte(g), toByte(b))
}

func cube(x float64) float64 { return x * x * x }

func toLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func toByte(c float64) int {
	if c <= 0.0031308 {
		c *= 12.92
	} else {
		c = 1.055*math.Pow(c, 1/2.4) - 0.055
	}
	return int(math.Round(math.Max(0, math.Min(1, c)) * 255))
}

// LogColorPalette is the colours offered in an application's settings: twelve
// hues evenly round the circle at the lightness and chroma Quasar colours
// applications with, so a colour picked there sits with the others.
func LogColorPalette() []string {
	out := make([]string, 0, 12)
	for i := range 12 {
		out = append(out, oklchHex(logColorLightness, logColorChroma, math.Mod(firstLogHue+float64(i)*30, 360)))
	}
	return out
}
