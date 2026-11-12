// adapterbrowser smoke-checks every live catalog resource through agent-browser.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/adapters"
)

type catalogDocument struct {
	ID string `json:"id"`
}

func main() {
	native := flag.String("native-catalog", adapters.NativeResourceCatalogURL, "published native resource catalog the web UI loads")
	flag.Parse()
	documents := append(fetchCatalog("http://localhost:9020/api/captain/ai/adapter-schemas"), fetchCatalog(*native)...)
	for _, document := range documents {
		parts := strings.SplitN(document.ID, "/", 2)
		if len(parts) != 2 {
			panic("invalid resource id: " + document.ID)
		}
		location := "http://localhost:9020/adapter-schemas/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "?view=form"
		browser("open", location)
		browser("wait", "--text", "Local preview.")
		body := browser("get", "text", "body")
		if strings.Contains(body, "Something went wrong") || !strings.Contains(body, "Validate schema") {
			panic(fmt.Sprintf("%s form failed: %s", document.ID, body))
		}
		fmt.Println("PASS " + document.ID)
	}
	fmt.Printf("PASS all %d resource forms\n", len(documents))
}

func browser(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "agent-browser", append([]string{"--engine", "chrome", "--session", "captain-adapters-impl"}, args...)...)
	raw, err := command.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("agent-browser %v: %v\n%s", args, err, raw))
	}
	return string(raw)
}

func fetchCatalog(location string) []catalogDocument {
	response, err := (&http.Client{Timeout: 20 * time.Second}).Get(location)
	check(err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		panic(location + ": " + response.Status)
	}
	var documents []catalogDocument
	check(json.NewDecoder(response.Body).Decode(&documents))
	if len(documents) == 0 {
		panic("empty resource catalog: " + location)
	}
	return documents
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
