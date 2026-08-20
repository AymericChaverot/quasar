// Package updater checks GitHub Releases for a newer Quasar version and
// records what it finds; the actual self-update is performed by the docker
// package through a short-lived updater container.
package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/notify"
	"quasar/internal/version"
)

// CheckInterval is how often the background checker asks GitHub for the latest
// release. Exported because the System page tells the operator this cadence,
// and a card claiming a frequency the checker does not keep is worse than one
// that says nothing.
//
// Kept well inside GitHub's 60-requests-per-hour anonymous limit — two calls an
// hour leaves the rest of that budget to the manual "Check for updates" button.
const CheckInterval = 30 * time.Minute

// Settings keys owned by the updater.
const (
	SettingLatestTag = "update_latest_tag"
	SettingCheckedAt = "update_checked_at"
)

var client = &http.Client{Timeout: 15 * time.Second}

// Check queries the latest GitHub release tag for repo ("owner/name") and
// stores the result. It returns the latest tag.
func Check(ctx context.Context, database *sql.DB, repo string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no releases found for %s", repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	// The tag is the answer; the two settings are the cache of it. A cache
	// that will not write means the next page re-asks GitHub, which is slower
	// and not wrong.
	if err := db.SetSetting(database, SettingLatestTag, release.TagName); err != nil {
		log.Printf("updater: caching the latest tag: %v", err)
	}
	if err := db.SetSetting(database, SettingCheckedAt, time.Now().Format(time.RFC3339)); err != nil {
		log.Printf("updater: caching the check time: %v", err)
	}
	return release.TagName, nil
}

// IsNewer compares two version tags ("v1.2.3"). A "dev" build never reports
// updates automatically — updating from dev is always an explicit choice.
func IsNewer(current, latest string) bool {
	if latest == "" || current == "dev" {
		return false
	}
	cur, lat := parse(current), parse(latest)
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

func parse(tag string) [3]int {
	var out [3]int
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	for i, part := range strings.SplitN(tag, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(strings.SplitN(part, "-", 2)[0])
		out[i] = n
	}
	return out
}

// StartChecker polls for new releases and notifies once per new tag.
//
// The first check runs shortly after boot rather than a full interval later.
// The header carries the update button now, so a dashboard that has just been
// started — which is exactly when someone is looking at it — would otherwise
// stay silent about a release that is already out. The short delay is only to
// keep the check off the critical path of coming up.
func StartChecker(database *sql.DB, repo string) {
	go func() {
		time.Sleep(15 * time.Second)
		for {
			known := db.GetSetting(database, SettingLatestTag)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			latest, err := Check(ctx, database, repo)
			cancel()
			// Notify once per tag: only a tag we had not seen before is news,
			// and the checker now runs often enough that repeating it would
			// mean a message every half hour until the update is applied.
			if err == nil && latest != known && IsNewer(version.Version, latest) {
				notify.Send(database, fmt.Sprintf("Quasar: version %s is available (current: %s). Update from the System page.", latest, version.Version))
			}
			time.Sleep(CheckInterval)
		}
	}()
}
