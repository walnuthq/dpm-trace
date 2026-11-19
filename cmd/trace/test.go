package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/config"
	"github.com/walnuthq/dpm-trace/internal/integration"
	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/render"
	"github.com/walnuthq/dpm-trace/internal/scaffold"
	"github.com/walnuthq/dpm-trace/internal/source"
	"github.com/walnuthq/dpm-trace/internal/testrunner"
)

// runTest runs a package's Daml Script unit tests. Ports run_test.
//
// It is a CI gate: any non-passing test must exit non-zero.
func runTest(args []string) int {
	if wantsHelp(args) {
		commandHelp(os.Stdout, "dpm trace test <package-dir> [flags]",
			"Run Daml Script unit tests, or lit integration tests against a local Canton that dpm trace boots (or persistent participants given by --ledger-url), and map failures to source.",
			testFlags, "")
		return 0
	}

	opts := testrunner.Options{MaxSourceLocations: 6}
	var (
		initMode        bool
		integrationOpts integration.Options
		scaffoldOpts    scaffold.Options
		root            string
		colorMode       = "auto"
		printJSON       bool
		noTrees         bool
		configPath      string
		damlYAML        []string
		darPaths        []string
		damlc           string
		debugInfo       []string
		sourceRoots     []string
	)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--print-json":
			printJSON = true
			continue
		case "--no-trees":
			noTrees = true
			continue
		case "--keep-artifacts":
			opts.KeepArtifacts = true
			continue
		case "--init":
			initMode = true
			continue
		case "--no-unittests":
			scaffoldOpts.NoUnittests = true
			continue
		case "--no-ci":
			scaffoldOpts.NoCI = true
			continue
		case "--verbose":
			integrationOpts.Verbose = true
			continue
		}
		// An empty argument is the package root (cwd), as run_test treats it;
		// indexing arg[0] here would panic.
		if !strings.HasPrefix(arg, "-") {
			if root != "" {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", arg)
				return 2
			}
			root = arg
			continue
		}
		if i+1 >= len(args) {
			fmt.Fprintf(os.Stderr, "error: %s requires a value\n", arg)
			return 2
		}
		i++
		value := args[i]
		switch arg {
		case "--integration":
			integrationOpts.TestDir = value
		case "--canton-jar":
			integrationOpts.CantonJar = value
			integrationOpts.CantonJarGiven = true
		case "--ledger-url":
			integrationOpts.LedgerURLs = append(integrationOpts.LedgerURLs, value)
		case "--token-file":
			integrationOpts.TokenFiles = append(integrationOpts.TokenFiles, value)
		case "--lit":
			integrationOpts.Lit = value
		case "--parties":
			integrationOpts.Parties = value
		case "--sequencer-type":
			integrationOpts.SequencerType = value
		case "--canton-timeout":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: --canton-timeout must be a number: %q\n", value)
				return 2
			}
			integrationOpts.CantonTimeout = parsed
		case "--itests-dir":
			scaffoldOpts.ITestsDir = value
		case "--unittests-dir":
			scaffoldOpts.UnitTestsDir = value
		case "--daml":
			opts.Daml = value
		case "-p", "--test-pattern":
			opts.TestPattern = value
		case "--files":
			opts.Files = append(opts.Files, value)
		case "--junit":
			opts.JUnitOut = value
		case "--daml-yaml":
			damlYAML = append(damlYAML, value)
		case "--color":
			colorMode = value
		case "--config":
			configPath = value
		case "--max-source-locations":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: --max-source-locations must be a number: %q\n", value)
				return 2
			}
			opts.MaxSourceLocations = parsed
		case "--dar":
			// Unit mode inspects every DAR; --integration deploys the first instead of building.
			darPaths = append(darPaths, value)
		case "--damlc":
			damlc = value
		case "--debug-info":
			debugInfo = append(debugInfo, value)
		case "--source-root":
			sourceRoots = append(sourceRoots, value)
		default:
			fmt.Fprintf(os.Stderr, "error: unknown flag %q\n", arg)
			return 2
		}
	}

	if integrationOpts.TestDir == "" && (len(integrationOpts.LedgerURLs) > 0 || len(integrationOpts.TokenFiles) > 0) {
		fmt.Fprintln(os.Stderr, "error: --ledger-url and --token-file apply only with --integration")
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		// 1 is reserved for "tests failed"; operational errors exit 2 so the
		// CI gate can tell a red suite from a broken invocation.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	darPaths = config.Strings(darPaths, cfg, "DPM_TRACE_DAR", "darPaths", "dar_paths", "dar")
	damlc = config.String(damlc, cfg, "DPM_TRACE_DAMLC", "damlc")
	debugInfo = config.Strings(debugInfo, cfg, "DPM_TRACE_DEBUG_INFO", "debugInfoPaths", "debug_info_paths", "debugInfo", "debug_info")
	sourceRoots = config.Strings(sourceRoots, cfg, "DPM_TRACE_SOURCE_ROOT", "sourceRoots", "source_roots", "sourceRoot")
	damlYAML = config.Strings(damlYAML, cfg, "DPM_TRACE_DAML_YAML", "damlYamlPaths", "daml_yaml_paths", "damlYaml", "daml_yaml")

	if root == "" {
		root = "."
	}
	// Path.resolve() in Python follows symlinks, which matters where a temp
	// directory is reached through one: the reported package path must match.
	absolute, resolveErr := resolvePath(root)
	if err = resolveErr; err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	opts.Root = absolute

	if integrationOpts.TestDir != "" {
		integrationOpts.Root = absolute
		integrationOpts.Daml = opts.Daml
		integrationOpts.DAR = firstOrEmpty(darPaths)
		integrationOpts.KeepArtifacts = opts.KeepArtifacts
		// %dpm runs this binary; the interpreter and source tree are forwarded
		// only for suites whose own scripts need them.
		integrationOpts.Python = os.Getenv("DPM_TRACE_IT_PYTHON")
		if integrationOpts.Python == "" {
			integrationOpts.Python = os.Getenv("DPM_TRACE_PYTHON")
		}
		integrationOpts.SrcRoot = os.Getenv("DPM_TRACE_IT_SRC")
		if integrationOpts.CantonTimeout == 0 {
			integrationOpts.CantonTimeout = 180
		}
		code, err := integration.Run(os.Stdout, os.Stderr, integrationOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return code
		}
		return code
	}

	if initMode {
		scaffoldOpts.Root = absolute
		result, err := scaffold.Init(os.Stdout, scaffoldOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		render.ScaffoldReport(os.Stdout, absolute, result,
			render.ColorFromMode(colorMode, isTTY()),
			orDefaultString(scaffoldOpts.ITestsDir, "itests"),
			orDefaultString(scaffoldOpts.UnitTestsDir, "unittests"))
		return 0
	}

	damlYAMLPath := filepath.Join(absolute, "daml.yaml")
	if _, err := os.Stat(damlYAMLPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: no daml.yaml found in %s; pass a package directory\n", absolute)
		return 2
	}
	if len(damlYAML) == 0 {
		damlYAML = []string{damlYAMLPath}
	}

	index := source.NewIndex()
	for _, path := range debugInfo {
		index.LoadDebugInfo(path)
	}
	for _, path := range damlYAML {
		index.LoadDamlYAML(path)
	}
	for _, root := range sourceRoots {
		index.LoadSourceRoot(root)
	}
	// run_test defaults --damlc to --daml, so a package built with one
	// toolchain is inspected with the same one.
	for _, path := range darPaths {
		index.LoadDARInspect(path, orDefaultString(damlc, orDefaultString(opts.Daml, "dpm")))
	}

	result, err := testrunner.Run(os.Stderr, opts, index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		return 2
	}

	if printJSON {
		encoded, err := model.Encode(testrunner.ReportJSON(absolute, result.Command, result.Cases, opts.MaxSourceLocations))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		fmt.Println(string(encoded))
	} else {
		color := render.ColorFromMode(colorMode, isTTY())
		render.TestReport(os.Stdout, absolute, result.Command, result.Cases, color, index, !noTrees)
		if !result.TreesAvailable && !noTrees {
			fmt.Printf("\n%s transaction trees were unavailable in this environment; showing results only.\n",
				color.Apply("note:", "gray"))
		}
	}

	if opts.KeepArtifacts && !printJSON {
		fmt.Printf("\nartifacts kept in: %s\n", result.WorkDir)
	}
	if result.Failed() {
		return 1
	}
	return 0
}

var testFlags = []flagDoc{
	{"--daml PATH", "Toolchain to run: dpm, daml or damlc. Defaults to dpm."},
	{"-p, --test-pattern PAT", "Run only tests matching a pattern."},
	{"--files PATH", "Restrict to specific source files. Repeatable."},
	{"--junit PATH", "Also write JUnit XML for CI."},
	{"--print-json", "Print the dpm-trace/test-report/v0 report and exit."},
	{"--no-trees", "Summary and failures only, for compact CI logs."},
	{"--daml-yaml PATH", "daml.yaml for source diagnostics. Repeatable. Defaults to the package's."},
	{"--source-root PATH", "Local Daml source root for diagnostics. Repeatable."},
	{"--debug-info PATH", "daml-debug-info/v1 file for source positions. Repeatable."},
	{"--max-source-locations N", "Maximum diagnostics per failure. Defaults to 6."},
	{"--keep-artifacts", "Keep the temporary run directory. Ignored with --ledger-url."},
	{"--dar PATH", "Local DAR to verify failures with damlc inspect. Repeatable."},
	{"--damlc PATH", "dpm, daml or damlc executable for inspection. Defaults to --daml."},
	{"--integration DIR", "Run the integration tests in DIR against a local Canton that dpm trace starts and stops, or against persistent participants with --ledger-url."},
	{"--canton-jar PATH", "Canton JAR to start for --integration. Defaults to $DPM_TRACE_CANTON_JAR."},
	{"--dar PATH", "With --integration, deploy the first DAR instead of building."},
	{"--ledger-url URL", "With --integration, attach to an existing participant's JSON Ledger API instead of booting Canton. Repeatable, one per participant: the Nth --ledger-url serves the parties placed with @N in --parties. Cannot be combined with --canton-jar."},
	{"--token-file PATH", "Requires --ledger-url. Bearer token file, re-read on every request. Repeatable: give none, or exactly one per --ledger-url in the same order."},
	{"--parties Name@N,...", "Parties to allocate and the participant each is placed on, e.g. Alice@1,Bob@2. Defaults to Alice,Bob."},
	{"--lit PATH", "lit executable for --integration. Defaults to lit."},
	{"--sequencer-type TYPE", "Sequencer type of the local Canton. Defaults to bft. Ignored with --ledger-url."},
	{"--canton-timeout SECONDS", "How long to wait for the local Canton to be ready, or with --ledger-url for each party to become visible on every participant. Defaults to 180."},
	{"--verbose", "Pass -vv to lit for per-test output."},
	{"--init", "Scaffold itests/, unittests/ and a CI workflow in the package instead of running tests."},
	{"--no-unittests", "With --init, skip unittests/."},
	{"--no-ci", "With --init, skip the CI workflow."},
	{"--itests-dir DIR", "With --init, the integration test directory. Defaults to itests."},
	{"--unittests-dir DIR", "With --init, the unit test directory. Defaults to unittests."},
	{"--config PATH", "Config JSON. Defaults to $DPM_TRACE_CONFIG (the integration runner's override), then .dpm-trace.json in this directory or a parent."},
	{"--color MODE", "auto, always or never. Defaults to auto."},
	{"-h, --help", "Show this help."},
}

func orDefaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// resolvePath makes a path absolute and follows symlinks, matching
// pathlib.Path.expanduser().resolve(). Ports resolve_package_root.
func resolvePath(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// A path that does not exist yet cannot be resolved; Python returns it
	// unchanged in that case too.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return absolute, nil
	}
	return resolved, nil
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
