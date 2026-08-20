package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/config"
	"github.com/walnuthq/dpm-trace/internal/ledger"
	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/render"
)

// runPrepare prepares a command without committing it. Ports run_prepare.
//
// This calls Canton's non-committing prepare API: nothing reaches the ledger,
// and the artifact must never be presented as a committed transaction.
func runPrepare(args []string) int {
	if wantsHelp(args) {
		commandHelp(os.Stdout, "dpm trace prepare --submitter <url> --act-as <party> --template <id> [flags]", "Prepare a command without committing it.", submissionFlags, "")
		return 0
	}
	opts, spec, rc := parseSubmissionFlags("prepare", args)
	if rc != 0 {
		return rc
	}

	commands, err := spec.Commands()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	actAs := ledger.ParseParties(opts.actAs)
	if len(actAs) == 0 {
		fmt.Fprintln(os.Stderr, "error: --act-as is required")
		return 1
	}
	readAs := excludeParties(ledger.ParseParties(append(opts.readAs, opts.party...)), actAs)
	if opts.ledgerURL == "" {
		fmt.Fprintln(os.Stderr, "error: --submitter/--participant-url/--ledger-url is required")
		return 1
	}

	commandID := opts.commandID
	if commandID == "" {
		commandID = ledger.GenerateCommandID("dpm-trace-prepare-")
	}
	request := map[string]any{
		"commandId":                    commandID,
		"commands":                     commands,
		"actAs":                        actAs,
		"readAs":                       readAs,
		"disclosedContracts":           []any{},
		"synchronizerId":               opts.synchronizerID,
		"packageIdSelectionPreference": []any{},
		"verboseHashing":               true,
	}
	if userID := ledger.UserID(opts.userID, opts.ledgerURL, opts.token, opts.tokenFile); userID != "" {
		request["userId"] = userID
	}

	token, err := ledger.ResolveToken(opts.token, opts.tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	response, url, err := ledger.New(opts.ledgerURL, token).Prepare(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	artifact := model.NewPreparedArtifact(request, response, url, opts.ledgerURL, actAs, readAs)
	encoded, err := model.Encode(artifact)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if opts.export != "" {
		if err := os.WriteFile(opts.export, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("wrote prepared artifact: %s\n", opts.export)
	}
	if opts.printJSON {
		fmt.Println(string(encoded))
		return 0
	}
	fmt.Println(render.PreparedArtifactSummary(artifact))
	return 0
}

func excludeParties(parties, exclude []string) []string {
	skip := map[string]bool{}
	for _, party := range exclude {
		skip[party] = true
	}
	out := []string{}
	for _, party := range parties {
		if !skip[party] {
			out = append(out, party)
		}
	}
	return out
}

// submissionOpts are the flags prepare and submit share.
type submissionOpts struct {
	ledgerURL      string
	actAs          []string
	readAs         []string
	party          []string
	token          string
	tokenFile      string
	configPath     string
	commandID      string
	userID         string
	synchronizerID string
	export         string
	logFile        []string
	damlYAML       []string
	sourceRoots    []string
	debugInfo      []string
	dar            []string
	damlc          string
	scanURL        string
	verbose        bool
	waitSeconds    int
	maxSourceLoc   int
	printJSON      bool
	allowFail      bool
	full           bool
	colorMode      string
}

func parseSubmissionFlags(name string, args []string) (submissionOpts, ledger.CommandSpec, int) {
	opts := submissionOpts{colorMode: "auto", maxSourceLoc: 5}
	var spec ledger.CommandSpec

	needsValue := func(i int, flag string) (string, bool) {
		if i+1 >= len(args) {
			fmt.Fprintf(os.Stderr, "error: %s requires a value\n", flag)
			return "", false
		}
		return args[i+1], true
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		var value string
		var ok bool
		switch arg {
		case "--print-json":
			opts.printJSON = true
			continue
		case "--allow-fail":
			opts.allowFail = true
			continue
		case "-v", "--verbose":
			opts.verbose = true
			continue
		case "--full":
			opts.full = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if value, ok = needsValue(i, arg); !ok {
				return opts, spec, 2
			}
			i++
		}
		switch arg {
		case "--submitter", "--participant-url", "--ledger-url":
			opts.ledgerURL = value
		case "--act-as":
			opts.actAs = append(opts.actAs, value)
		case "--read-as":
			opts.readAs = append(opts.readAs, value)
		case "--party":
			opts.party = append(opts.party, value)
		case "--token":
			opts.token = value
		case "--token-file", "--access-token-file":
			opts.tokenFile = value
		case "--config":
			opts.configPath = value
		case "--command-id":
			opts.commandID = value
		case "--user-id":
			opts.userID = value
		case "--synchronizer-id":
			opts.synchronizerID = value
		case "--export":
			opts.export = value
		case "--log-file":
			opts.logFile = append(opts.logFile, value)
		case "--daml-yaml":
			opts.damlYAML = append(opts.damlYAML, value)
		case "--source-root":
			opts.sourceRoots = append(opts.sourceRoots, value)
		case "--debug-info":
			opts.debugInfo = append(opts.debugInfo, value)
		case "--dar":
			opts.dar = append(opts.dar, value)
		case "--damlc":
			opts.damlc = value
		case "--scan-url":
			opts.scanURL = value
		case "--out":
			// Alias of --export, as add_common_connection_args declares it.
			opts.export = value
		case "--wait":
			if n, err := strconv.Atoi(value); err == nil {
				opts.waitSeconds = n
			}
		case "--max-source-locations":
			if n, err := strconv.Atoi(value); err == nil {
				opts.maxSourceLoc = n
			}
		case "--color":
			opts.colorMode = value
		case "--template":
			spec.Template = value
		case "--contract-id":
			spec.ContractID = value
		case "--choice":
			spec.Choice = value
		case "--arg":
			spec.Args = append(spec.Args, value)
		case "--args-json":
			spec.ArgsJSON = value
		case "--args-file":
			spec.ArgsFile = value
		case "--commands":
			spec.CommandsFile = value
		case "--command-json":
			spec.CommandJSON = value
		default:
			fmt.Fprintf(os.Stderr, "error: unknown flag %q for dpm trace %s\n", arg, name)
			return opts, spec, 2
		}
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return opts, spec, 1
	}
	opts.ledgerURL = config.String(opts.ledgerURL, cfg, "DPM_TRACE_LEDGER_URL", "ledgerUrl", "ledger_url")
	opts.token = config.String(opts.token, cfg, "DPM_TRACE_TOKEN", "token")
	opts.tokenFile = config.String(opts.tokenFile, cfg, "DPM_TRACE_TOKEN_FILE", "tokenFile", "token_file")
	// apply_config_defaults only fills read-as when neither --read-as nor
	// --party was given; a party from either flag suppresses the config value.
	if len(opts.readAs) == 0 && len(opts.party) == 0 {
		opts.readAs = config.Strings(nil, cfg, "DPM_TRACE_READ_AS", "readAs", "read_as", "party")
	}
	return opts, spec, 0
}
