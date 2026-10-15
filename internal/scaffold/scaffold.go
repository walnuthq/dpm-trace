// Package scaffold implements `dpm trace test --init`, which writes the itests/
// and unittests/ layout and a CI workflow into a user's Daml package.
//
// The generated lit.cfg.py stays Python: it configures lit, which is a Python
// tool. Only the code that emits it moves to Go. Templates live as real files
// under templates/ and are embedded, so they stay readable and diffable against
// the canonical sibling copies the scaffolder-sync test checks.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Options configure a scaffold run.
type Options struct {
	Root         string
	ITestsDir    string
	UnitTestsDir string
	NoUnittests  bool
	NoCI         bool
}

// fields are the substitutions the templates take.
type fields struct {
	PackageName   string
	SDKVersion    string
	ITestsDir     string
	UnitTestsDir  string
	WithUnittests bool
}

// Result reports what the run did. Existing files are kept, never overwritten:
// --init is expected to be re-runnable on a package that already has some of
// the layout.
type Result struct {
	Created []string
	Kept    []string
}

// Init writes the test layout. Ports run_init.
func Init(w io.Writer, opts Options) (Result, error) {
	var result Result

	damlYAML := filepath.Join(opts.Root, "daml.yaml")
	if _, err := os.Stat(damlYAML); err != nil {
		return result, fmt.Errorf("%s is not a Daml package (no daml.yaml found)", opts.Root)
	}

	data := fields{
		PackageName:   fieldOr(damlYAML, "name", "app"),
		SDKVersion:    fieldOr(damlYAML, "sdk-version", "3.5.1"),
		ITestsDir:     orDefault(opts.ITestsDir, "itests"),
		UnitTestsDir:  orDefault(opts.UnitTestsDir, "unittests"),
		WithUnittests: !opts.NoUnittests,
	}

	write := func(rel, templateName string) error {
		path := filepath.Join(opts.Root, rel)
		if _, err := os.Stat(path); err == nil {
			result.Kept = append(result.Kept, rel)
			return nil
		}
		rendered, err := render(templateName, data)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			return err
		}
		result.Created = append(result.Created, rel)
		return nil
	}

	if err := write(filepath.Join(data.ITestsDir, "lit.cfg.py"), "lit.cfg.py.tmpl"); err != nil {
		return result, err
	}
	if err := write(filepath.Join(data.ITestsDir, "example.test"), "example.test.tmpl"); err != nil {
		return result, err
	}
	if !opts.NoUnittests {
		if err := write(filepath.Join(data.UnitTestsDir, "daml.yaml"), "unittests-daml.yaml.tmpl"); err != nil {
			return result, err
		}
		if err := write(filepath.Join(data.UnitTestsDir, "daml", "Example.daml"), "example.daml.tmpl"); err != nil {
			return result, err
		}
	}
	if !opts.NoCI {
		if err := write(filepath.Join(".github", "workflows", "dpm-trace.yml"), "ci.yml.tmpl"); err != nil {
			return result, err
		}
	}
	return result, nil
}

func render(name string, data fields) ([]byte, error) {
	raw, err := templates.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	// Templates contain Daml and shell syntax; only the {{...}} fields are
	// substituted, so no functions or escaping are configured.
	parsed, err := template.New(name).Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fieldOr reads a scalar field from a daml.yaml, falling back when absent.
// Ports read_daml_yaml_field.
func fieldOr(path, field, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	pattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(field) + `\s*:\s*(.+?)\s*$`)
	for _, line := range strings.Split(string(data), "\n") {
		if match := pattern.FindStringSubmatch(line); match != nil {
			return stripYAMLScalar(match[1])
		}
	}
	return fallback
}

func stripYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' || first == '\'') && first == last {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
