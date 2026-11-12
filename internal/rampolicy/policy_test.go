package rampolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDraftLevelsAndDeploymentCopies(t *testing.T) {
	catalog := Catalog()
	if len(catalog) != 3 {
		t.Fatal("missing policy tier")
	}
	prior := map[string]bool{}
	for index, draft := range catalog {
		if draft.Verification != "draft" || len(draft.Limitations) < 3 || draft.CloudWritePermissions != (index > 0) {
			t.Fatal("draft represented as verified or read-only", draft.ID)
		}
		actions := map[string]bool{}
		for _, statement := range draft.Document.Statement {
			if statement.Effect != "Allow" || statement.Resource == "" {
				t.Fatal("invalid statement", statement)
			}
			for _, action := range statement.Action {
				if actions[action] || strings.Contains(action, "*") || strings.HasPrefix(action, "ram:") || strings.Contains(action, "Command") || strings.Contains(action, "Invocation") || strings.HasPrefix(action, "cdt:") {
					t.Fatal("unexpected privilege", action)
				}
				if index == 0 {
					operation := strings.SplitN(action, ":", 2)[1]
					if !strings.HasPrefix(operation, "Describe") && !strings.HasPrefix(operation, "List") && !strings.HasPrefix(operation, "Query") {
						t.Fatal("read-only tier grants writes", action)
					}
				}
				actions[action] = true
			}
		}
		if len(actions) != []int{12, 18, 26}[index] {
			t.Fatal("unexpected tier action count", draft.ID, len(actions))
		}
		for action := range prior {
			if !actions[action] {
				t.Fatal("higher tier lost a lower-tier action", draft.ID, action)
			}
		}
		prior = actions
		raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "ram", draft.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.MarshalIndent(draft.Document, "", "  ")
		if err != nil || !bytes.Equal(raw, append(encoded, '\n')) {
			t.Fatal("deployment policy differs from API; run go run ./internal/rampolicy/export ./deploy/ram", draft.ID, err)
		}
	}
	// Mutating a returned document cannot change a later response.
	catalog[0].Document.Statement[0].Action[0] = "invalid"
	if Catalog()[0].Document.Statement[0].Action[0] == "invalid" {
		t.Fatal("catalog shares global mutable policy data")
	}
}

func TestWriteActionsHaveOfficialEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "m0", "ram-write-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		APIs []struct {
			Source  string `json:"source"`
			Actions []struct {
				Action struct {
					Name      string `json:"action"`
					Resources []struct {
						ARN string `json:"arn"`
					} `json:"resources"`
				} `json:"ramAction"`
			} `json:"ram_actions"`
		} `json:"apis"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	known := map[string][]string{}
	placeholder := regexp.MustCompile(`\{#[^}]+\}`)
	for _, api := range evidence.APIs {
		if !strings.HasPrefix(api.Source, "https://api.aliyun.com/meta/v1/products/") {
			t.Fatal("non-official evidence", api.Source)
		}
		for _, action := range api.Actions {
			for _, resource := range action.Action.Resources {
				known[action.Action.Name] = append(known[action.Action.Name], placeholder.ReplaceAllString(resource.ARN, "*"))
			}
		}
	}
	if len(known) != 14 {
		t.Fatal("incomplete write action evidence", len(known))
	}
	read := map[string]bool{}
	for _, statement := range Catalog()[0].Document.Statement {
		for _, action := range statement.Action {
			read[action] = true
		}
	}
	for _, statement := range Catalog()[2].Document.Statement {
		for _, action := range statement.Action {
			if !read[action] && len(known[action]) == 0 {
				t.Fatal("write permission without evidence", action)
			}
			if !read[action] && statement.Resource != "*" {
				matched := false
				for _, resource := range known[action] {
					matched = matched || resource == statement.Resource
				}
				if !matched {
					t.Fatal("resource pattern not supported by evidence", action, statement.Resource)
				}
			}
		}
	}
}
