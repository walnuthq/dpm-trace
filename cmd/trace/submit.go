package main

import (
	"fmt"
	"os"

	"github.com/walnuthq/dpm-trace/internal/ledger"
	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/render"
	"github.com/walnuthq/dpm-trace/internal/source"
)

// runSubmit submits a command and prints the resulting update id.
// Ports run_submit.
//
// A rejected submission has no update id, so the completion view is printed
// instead of a transaction tree. --allow-fail makes that a success exit, which
// is how integration tests capture a rejection.
func runSubmit(args []string) int {
	if wantsHelp(args) {
		commandHelp(os.Stdout, "dpm trace submit --submitter <url> --act-as <party> --template <id> [flags]", "Submit a command and print the resulting update id.", submitFlags, "")
		return 0
	}
	opts, spec, rc := parseSubmissionFlags("submit", args)
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
		commandID = ledger.GenerateCommandID("dpm-trace-submit-")
	}
	request := map[string]any{
		"commandId": commandID,
		"commands":  commands,
		"actAs":     actAs,
		"readAs":    readAs,
	}
	if opts.synchronizerID != "" {
		request["synchronizerId"] = opts.synchronizerID
	}
	if userID := ledger.UserID(opts.userID, opts.ledgerURL, opts.token, opts.tokenFile); userID != "" {
		request["userId"] = userID
	}

	token, err := ledger.ResolveToken(opts.token, opts.tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	ok, response, _, err := ledger.New(opts.ledgerURL, token).SubmitAndWait(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !ok {
		if code := writeSubmitExport(opts.export, response); code != 0 {
			return code
		}
		if opts.printJSON {
			encoded, encodeErr := model.Encode(response)
			if encodeErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", encodeErr)
				return 1
			}
			fmt.Println(string(encoded))
		} else {
			completion := &model.Completion{Raw: model.NormalizeCompletion(response)}
			if completion.String("commandId", "command_id") == "" {
				completion.Raw.Set("commandId", commandID)
			}
			// The index and log matches are built before choosing compact or
			// full, as run_submit does: --full previously discarded exactly
			// the diagnostics --daml-yaml and --log-file were asking for.
			index := source.NewIndex()
			for _, path := range opts.debugInfo {
				index.LoadDebugInfo(path)
			}
			for _, path := range opts.damlYAML {
				index.LoadDamlYAML(path)
			}
			for _, root := range opts.sourceRoots {
				index.LoadSourceRoot(root)
			}
			for _, path := range opts.dar {
				index.LoadDARInspect(path, orDefaultString(opts.damlc, "daml"))
			}
			completion.Raw = model.AttachLogMatches(completion.Raw, opts.logFile)
			color := render.ColorFromMode(opts.colorMode, isTTY())
			if opts.full {
				render.CompletionTrace(os.Stdout, completion, color, index, opts.maxSourceLoc)
			} else {
				render.SubmitFailure(os.Stdout, completion, request, opts.export, color, index, opts.maxSourceLoc)
			}
		}
		if opts.allowFail {
			return 0
		}
		return 1
	}

	if code := writeSubmitExport(opts.export, response); code != 0 {
		return code
	}
	if opts.printJSON {
		encoded, err := model.Encode(response)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(string(encoded))
		return 0
	}

	updateID := model.ObjectString(response, "updateId")
	if updateID == "" {
		// Include the body: without it there is nothing to debug, since the
		// call succeeded and only the shape was unexpected.
		detail := ""
		if encoded, err := model.Encode(response); err == nil {
			detail = ": " + string(encoded)
		}
		fmt.Fprintf(os.Stderr, "error: submit-and-wait returned no updateId%s\n", detail)
		return 1
	}
	fmt.Println(updateID)
	return 0
}

// writeSubmitExport saves the participant's response. A rejection is the case
// that matters: --completion-file reads the file back, and without this the
// only way to keep one was to redirect --print-json.
func writeSubmitExport(path string, response *model.Object) int {
	if path == "" {
		return 0
	}
	encoded, err := model.Encode(response)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	return 0
}
