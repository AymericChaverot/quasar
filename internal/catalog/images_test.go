package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Every image the catalogue names has to still exist in its registry. This is
// the cheap half of TestDeployEveryTemplate: it needs no Docker and no disk,
// only the network, so it catches an image that was renamed or withdrawn long
// before anyone tries to deploy it.
//
//	CATALOG_IMAGES=1 go test ./internal/catalog/ -run TestImages
func TestEveryImageStillExists(t *testing.T) {
	if os.Getenv("CATALOG_IMAGES") == "" {
		t.Skip("set CATALOG_IMAGES=1 to check every image against its registry")
	}

	refs := allImageRefs(t)
	if len(refs) < len(Templates) {
		t.Fatalf("only found %d image refs for %d templates", len(refs), len(Templates))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	for ref, where := range refs {
		wg.Add(1)
		go func(ref, where string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := manifestExists(ref); err != nil {
				mu.Lock()
				t.Errorf("%s (%s): %v", ref, where, err)
				mu.Unlock()
			}
		}(ref, where)
	}
	wg.Wait()
}

// allImageRefs collects every image the catalogue names, from the image
// entries and from inside every compose file, mapped to the entry it came from.
func allImageRefs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range Templates {
		if e.ImageRef != "" {
			out[e.ImageRef] = e.ID
		}
		if e.Compose == "" {
			continue
		}
		var doc struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(e.Compose), &doc); err != nil {
			t.Fatalf("%s: compose does not parse: %v", e.ID, err)
		}
		for _, svc := range doc.Services {
			if svc.Image != "" {
				out[svc.Image] = e.ID
			}
		}
	}
	return out
}

var authRe = regexp.MustCompile(`realm="([^"]+)"|service="([^"]+)"`)

// manifestExists asks the registry for the tag's manifest, negotiating an
// anonymous pull token the way a docker client would.
func manifestExists(ref string) error {
	registry, repo, tag := splitRef(ref)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)
	accept := strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ",")

	client := &http.Client{Timeout: 30 * time.Second}
	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}

	resp, err := do("")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		var realm, service string
		for _, m := range authRe.FindAllStringSubmatch(resp.Header.Get("Www-Authenticate"), -1) {
			if m[1] != "" {
				realm = m[1]
			}
			if m[2] != "" {
				service = m[2]
			}
		}
		if realm == "" {
			return fmt.Errorf("401 with no auth realm to follow")
		}
		tokURL := fmt.Sprintf("%s?scope=repository:%s:pull", realm, repo)
		if service != "" {
			tokURL += "&service=" + service
		}
		tokResp, err := client.Get(tokURL)
		if err != nil {
			return err
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		err = json.NewDecoder(tokResp.Body).Decode(&tok)
		tokResp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding pull token: %w", err)
		}
		token := tok.Token
		if token == "" {
			token = tok.AccessToken
		}
		resp2, err := do(token)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		resp = resp2
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry answered %s", resp.Status)
	}
	return nil
}

// splitRef breaks "ghcr.io/org/app:tag" into its registry, repository and tag,
// defaulting to Docker Hub and to "latest" the way the docker CLI does.
func splitRef(ref string) (registry, repo, tag string) {
	name := ref
	tag = "latest"
	// A colon in the first segment is a registry port, not a tag separator.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	registry, repo = "registry-1.docker.io", name
	first := strings.Split(name, "/")[0]
	switch {
	case strings.Contains(first, "."):
		registry, repo = first, strings.TrimPrefix(name, first+"/")
	case !strings.Contains(name, "/"):
		repo = "library/" + name
	}
	return registry, repo, tag
}
