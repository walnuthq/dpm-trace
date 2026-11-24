package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/walnuthq/dpm-trace/internal/config"
	"github.com/walnuthq/dpm-trace/internal/ledger"
	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/testrunner"
)

// Options configure an integration run. LedgerURLs lists one JSON Ledger API
// URL per participant, in order, and switches the run to attach mode.
// TokenFiles lists one bearer token file per URL, in the same order, or is empty
// for participants that need no authentication.
type Options struct {
	TestDir        string
	Root           string
	Daml           string
	CantonJar      string
	LedgerURLs     []string
	TokenFiles     []string
	CantonJarGiven bool
	Lit            string
	Parties        string
	DAR            string
	SequencerType  string
	CantonTimeout  int
	Verbose        bool
	KeepArtifacts  bool
	SrcRoot        string // exported to tests as DPM_TRACE_IT_SRC
	Python         string // exported to tests as DPM_TRACE_IT_PYTHON
}

// Run boots a Canton, deploys the DAR, allocates parties, runs lit against the
// live node and tears everything down. With LedgerURLs set it attaches to
// existing participants over the JSON Ledger API instead of booting one.
// Ports run_integration_tests.
//
// The lit exit code is returned unchanged: it is the CI gate. SIGINT,
// SIGTERM and SIGHUP received once setup begins, and a write to a closed
// stdout or stderr, stop lit, revoke any rights granted in attach mode, stop a
// booted Canton and return 128 plus the signal number (SIGPIPE for the write).
func Run(stdout, stderr io.Writer, opts Options) (code int, err error) {
	testDir, err := resolve(opts.TestDir)
	if err != nil {
		return 2, err
	}
	if _, err := os.Stat(testDir); err != nil {
		return 2, fmt.Errorf("integration test dir not found: %s", testDir)
	}

	daml := expandUser(orDefault(opts.Daml, "dpm"))
	lit := orDefault(opts.Lit, "lit")
	placements := ParsePlacements(orDefault(opts.Parties, "Alice,Bob"))
	if err := RejectReservedPartyNames(placements); err != nil {
		return 2, err
	}
	participants := MaxParticipant(placements)
	if err := ValidateAttach(opts, participants); err != nil {
		return 2, err
	}
	if len(opts.LedgerURLs) > 0 {
		if err := CheckSuiteForwardsRunnerConfig(testDir); err != nil {
			return 2, err
		}
	}

	// The suite invokes %dpm. The generated lit.cfg.py reads it from
	// DPM_TRACE_IT_DPM and fails without it, so point it at this binary.
	self, err := os.Executable()
	if err != nil {
		return 2, err
	}

	cantonJar := ""
	if len(opts.LedgerURLs) == 0 {
		if cantonJar, err = resolveCantonJar(opts.CantonJar); err != nil {
			return 2, err
		}
	}

	dar := opts.DAR
	if dar == "" {
		fmt.Fprintln(stdout, "building DAR ...")
		if dar, err = BuildDAR(opts.Root, daml); err != nil {
			return 2, err
		}
	}
	if dar, err = resolve(dar); err != nil {
		return 2, err
	}

	work, err := os.MkdirTemp("", "dpm-trace-itest-")
	if err != nil {
		return 2, err
	}
	attached := len(opts.LedgerURLs) > 0
	defer func() {
		if opts.KeepArtifacts && !attached {
			fmt.Fprintf(stdout, "\nkept Canton log + config: %s\n", work)
			return
		}
		os.RemoveAll(work)
	}()
	logPath := filepath.Join(work, "canton.log")
	runnerConfig := ""
	if attached {
		runnerConfig = filepath.Join(work, "dpm-trace.json")
		discovered := config.FindFrom(filepath.Join(testDir, ".lit"))
		if err := config.WriteWithoutConnection(discovered, runnerConfig); err != nil {
			return 2, err
		}
	}

	ctx, interruptedCode, brokenPipe, release := cancelOnInterrupt()
	defer release()
	defer func() {
		if signalCode := interruptedCode(); signalCode != 0 {
			code, err = signalCode, errors.New("interrupted")
		}
	}()
	litStdout, litStderr := stdout, stderr
	stdout, stderr = brokenPipeWatcher{stdout, brokenPipe}, brokenPipeWatcher{stderr, brokenPipe}
	failed := func(err error) (int, error) {
		if signalCode := interruptedCode(); signalCode != 0 {
			return signalCode, errors.New("interrupted")
		}
		return 2, err
	}

	var (
		participantURLs []string
		partyIDs        map[string]string
		tokenFiles      []string
		stop            func()
		grants          *Grants
	)
	if len(opts.LedgerURLs) > 0 {
		participantURLs = opts.LedgerURLs
		partyIDs, tokenFiles, grants, err = attachParticipants(ctx, stdout, opts, dar, placements)
		defer grants.Revoke(stderr)
		if err != nil {
			return failed(err)
		}
		logPath = ""
	} else {
		if participantURLs, partyIDs, stop, err = bootCanton(ctx, stdout, opts, work, cantonJar, dar, placements, participants); err != nil {
			return failed(err)
		}
		defer stop()
	}

	names := make([]string, 0, len(partyIDs))
	for name := range partyIDs {
		names = append(names, name)
	}
	sort.Strings(names)
	ready := make([]string, 0, len(names))
	for _, name := range names {
		ready = append(ready, name+"="+shortParty(partyIDs[name]))
	}
	if attached {
		fmt.Fprintln(stdout, "parties: "+strings.Join(ready, ", "))
	} else {
		fmt.Fprintln(stdout, "Canton ready: "+strings.Join(ready, ", "))
	}

	extra := map[string]string{
		"DPM_TRACE_IT_LEDGER":    participantURLs[0],
		"DPM_TRACE_IT_DAR":       dar,
		"DPM_TRACE_IT_DAML":      daml,
		"DPM_TRACE_IT_DAML_YAML": filepath.Join(opts.Root, "daml.yaml"),
		"DPM_TRACE_IT_DPM":       self,
	}
	if runnerConfig != "" {
		extra[config.EnvConfig] = runnerConfig
	}
	// Only forwarded when supplied, for suites whose own scripts need Python.
	if opts.Python != "" {
		extra["DPM_TRACE_IT_PYTHON"] = opts.Python
	}
	if opts.SrcRoot != "" {
		extra["DPM_TRACE_IT_SRC"] = opts.SrcRoot
	}
	for index := 2; index <= participants; index++ {
		extra[fmt.Sprintf("DPM_TRACE_IT_LEDGER%d", index)] = participantURLs[index-1]
	}
	for name, id := range partyIDs {
		extra["DPM_TRACE_IT_"+strings.ToUpper(name)] = id
	}
	for index, file := range tokenFiles {
		extra["DPM_TRACE_IT_TOKEN_FILE"+indexSuffix(index+1)] = file
	}

	fmt.Fprintf(stdout, "running integration suite: %s\n\n", testDir)
	litFlags := []string{"-v"}
	if opts.Verbose {
		litFlags = []string{"-vv"}
	}
	litCmd := exec.CommandContext(ctx, lit, append([]string{testDir}, litFlags...)...)
	runInOwnProcessGroup(litCmd)
	litCmd.WaitDelay = litShutdownGrace
	litCmd.Env = litEnvironment(attached, extra)
	litCmd.Stdout = litStdout
	litCmd.Stderr = litStderr
	litErr := litCmd.Run()
	if signalCode := interruptedCode(); signalCode != 0 {
		killStragglers(litCmd)
		return signalCode, errors.New("interrupted")
	}

	if litErr != nil {
		var exit *exec.ExitError
		if errors.As(litErr, &exit) {
			code = exit.ExitCode()
		} else {
			// lit missing or not executable: the suite never ran, so this is
			// an operational error rather than a failing suite. Reported here
			// because the output is otherwise indistinguishable from a red run.
			fmt.Fprintf(stderr, "error: %v\n", litErr)
			code = 2
		}
		if opts.Verbose && logPath != "" {
			fmt.Fprintf(stderr, "\n--- Canton log (last 60 lines) ---\n%s\n", ReadTail(logPath, 60))
		}
	}
	return code, nil
}

func litEnvironment(attached bool, extra map[string]string) []string {
	return filterEnvironment(testrunner.ChildEnv(extra), extra, attached, runtime.GOOS == "windows")
}

func filterEnvironment(env []string, extra map[string]string, attached, caseInsensitive bool) []string {
	canonical := func(key string) string {
		if caseInsensitive {
			return strings.ToUpper(key)
		}
		return key
	}
	supplied := make(map[string]bool, len(extra))
	for key := range extra {
		supplied[canonical(key)] = true
	}
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		_, exact := extra[key]
		if !exact && supplied[canonical(key)] {
			continue
		}
		if !supplied[canonical(key)] && isAmbientVariableToDrop(canonical(key), attached) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func isAmbientVariableToDrop(canonicalKey string, attached bool) bool {
	if strings.HasPrefix(canonicalKey, "DPM_TRACE_IT_TOKEN_FILE") {
		return true
	}
	return attached && (strings.HasPrefix(canonicalKey, "DPM_TRACE_IT_") ||
		strings.HasPrefix(canonicalKey, "DPM_TRACE_TOKEN") ||
		canonicalKey == "DPM_TRACE_LEDGER_URL" ||
		canonicalKey == "DPM_TRACE_SCAN_URL")
}

func indexSuffix(index int) string {
	if index == 1 {
		return ""
	}
	return strconv.Itoa(index)
}

func resolveCantonJar(flag string) (string, error) {
	cantonJar := flag
	if cantonJar == "" {
		cantonJar = os.Getenv("DPM_TRACE_CANTON_JAR")
	}
	if cantonJar != "" {
		cantonJar, _ = resolve(cantonJar)
	}
	if cantonJar == "" {
		return "", fmt.Errorf("--canton-jar (or DPM_TRACE_CANTON_JAR) must point at a Canton jar")
	}
	if _, err := os.Stat(cantonJar); err != nil {
		return "", fmt.Errorf("--canton-jar (or DPM_TRACE_CANTON_JAR) must point at a Canton jar")
	}
	return cantonJar, nil
}

func attachParticipants(ctx context.Context, stdout io.Writer, opts Options, dar string, placements []Placement) (map[string]string, []string, *Grants, error) {
	participants := make([]Participant, len(opts.LedgerURLs))
	var tokenFiles []string
	for i, ledgerURL := range opts.LedgerURLs {
		participants[i] = Participant{URL: ledgerURL}
		if len(opts.TokenFiles) == 0 {
			continue
		}
		tokenFile, err := filepath.Abs(opts.TokenFiles[i])
		if err != nil {
			return nil, nil, nil, err
		}
		participants[i].TokenFile = tokenFile
		tokenFiles = append(tokenFiles, tokenFile)
	}
	darBytes, err := os.ReadFile(dar)
	if err != nil {
		return nil, nil, nil, err
	}
	fmt.Fprintf(stdout, "attaching to %s; deploying %s ...\n", strings.Join(opts.LedgerURLs, ", "), filepath.Base(dar))
	partyIDs, grants, err := Attach(ctx, stdout, participants, darBytes, placements, RunSuffix(), time.Duration(opts.CantonTimeout)*time.Second)
	if err != nil {
		return nil, nil, grants, err
	}
	return partyIDs, tokenFiles, grants, nil
}

func bootCanton(ctx context.Context, stdout io.Writer, opts Options, work, cantonJar, dar string, placements []Placement, participants int) ([]string, map[string]string, func(), error) {
	logPath := filepath.Join(work, "canton.log")
	ports, err := FreePorts(3 + 3*participants)
	if err != nil {
		return nil, nil, nil, err
	}
	participantPorts := make([]ParticipantPorts, 0, participants)
	participantURLs := make([]string, 0, participants)
	for i := 0; i < participants; i++ {
		base := 3 + 3*i
		p := ParticipantPorts{Admin: ports[base], Ledger: ports[base+1], HTTP: ports[base+2]}
		participantPorts = append(participantPorts, p)
		participantURLs = append(participantURLs, fmt.Sprintf("http://127.0.0.1:%d", p.HTTP))
	}

	configPath := filepath.Join(work, "canton.conf")
	bootstrapPath := filepath.Join(work, "bootstrap.canton")
	if err := os.WriteFile(configPath, []byte(ConfigText(ports[0], ports[1], ports[2], participantPorts, orDefault(opts.SequencerType, "bft"))), 0o644); err != nil {
		return nil, nil, nil, err
	}
	if err := os.WriteFile(bootstrapPath, []byte(BootstrapText(dar, placements, participants)), 0o644); err != nil {
		return nil, nil, nil, err
	}

	endpoints := make([]string, 0, participants)
	for i, p := range participantPorts {
		endpoints = append(endpoints, fmt.Sprintf("participant%d :%d", i+1, p.HTTP))
	}
	fmt.Fprintf(stdout, "booting local Canton (%s); deploying %s ...\n",
		strings.Join(endpoints, ", "), filepath.Base(dar))

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, nil, nil, err
	}
	canton := exec.Command("java", "-jar", cantonJar, "daemon",
		"-c", configPath, "--bootstrap", bootstrapPath, "--no-tty")
	canton.Stdout = logFile
	canton.Stderr = logFile
	canton.Dir = work
	canton.Env = testrunner.ChildEnv(nil)
	if err := canton.Start(); err != nil {
		logFile.Close()
		return nil, nil, nil, err
	}
	stop := func() {
		logFile.Close()
		if canton.Process == nil {
			return
		}
		_ = canton.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = canton.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = canton.Process.Kill()
		}
	}

	partyURLs := make([]PartyEndpoint, 0, len(placements))
	for _, placement := range placements {
		partyURLs = append(partyURLs, PartyEndpoint{Name: placement.Name, LedgerURL: participantURLs[placement.Index-1]})
	}
	partyIDs, err := WaitForParties(ctx, partyURLs, canton, logPath, opts.CantonTimeout)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	return participantURLs, partyIDs, stop, nil
}

// PartyEndpoint ties a party to the participant that hosts it.
type PartyEndpoint struct {
	Name      string
	LedgerURL string
}

// WaitForParties polls until every party is visible on its participant, the
// timeout elapses or ctx is cancelled.
// Ports wait_for_parties.
func WaitForParties(ctx context.Context, placements []PartyEndpoint, canton *exec.Cmd, logPath string, timeout int) (map[string]string, error) {
	if timeout < 1 {
		timeout = 1
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	found := map[string]string{}
	lastError := ""

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// ProcessState is only populated by Wait, which nothing calls during
		// polling -- checking it here never fires, so a Canton that died on a
		// bad jar argument or a port clash used to poll connection-refused for
		// the full timeout. Signal 0 asks the OS whether the process is still
		// alive without touching it.
		if canton.Process != nil && canton.Process.Signal(syscall.Signal(0)) != nil {
			return nil, fmt.Errorf("canton exited during startup:\n%s", ReadTail(logPath, 40))
		}

		for _, placement := range placements {
			if _, ok := found[placement.Name]; ok {
				continue
			}
			id, err := lookupParty(placement)
			if err != nil {
				lastError = err.Error()
				continue
			}
			if id != "" {
				found[placement.Name] = id
			}
		}

		if len(found) == len(placements) {
			// The parties exist on the Ledger API, but their topology can take
			// a moment to become effective on the synchronizer; submitting
			// before then fails with UNKNOWN_INFORMEES.
			if err := sleep(ctx, 3*time.Second); err != nil {
				return nil, err
			}
			return found, nil
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("canton did not become ready within %ds (last error: %s)", timeout, lastError)
}

// lookupParty asks a participant for its parties and returns the full id of
// the named one, if it exists yet.
func lookupParty(placement PartyEndpoint) (string, error) {
	client := ledger.New(placement.LedgerURL, "")
	raw, err := client.JSON("GET", ledger.JoinURL(placement.LedgerURL, "/v2/parties"), nil, false)
	if err != nil {
		return "", err
	}
	value, ok := raw.Get("partyDetails")
	if !ok {
		return "", nil
	}
	details, ok := value.([]any)
	if !ok {
		return "", nil
	}

	prefix := placement.Name + "::"
	for _, item := range details {
		detail, isObject := item.(*model.Object)
		if !isObject {
			continue
		}
		if party := model.ObjectString(detail, "party"); strings.HasPrefix(party, prefix) {
			return party, nil
		}
	}
	return "", nil
}

// BuildDAR builds the package and returns the newest DAR produced.
// Ports build_dar.
func BuildDAR(root, daml string) (string, error) {
	// Only ~ is expanded: a bare name like "daml" must stay relative so it
	// resolves on PATH, which is how it is normally invoked.
	executable := expandUser(daml)
	name := filepath.Base(executable)

	command := []string{executable, "build"}
	switch name {
	case "damlc":
		command = []string{executable, "build", "--package-root", root}
	case "daml":
		command = append(command, "--no-legacy-assistant-warning")
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = root
	cmd.Env = testrunner.ChildEnv(map[string]string{"DAML_PACKAGE": root})
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("daml build failed:\n%s", output)
	}

	dist := filepath.Join(root, ".daml", "dist")
	entries, err := os.ReadDir(dist)
	if err != nil {
		return "", fmt.Errorf("daml build produced no DAR in %s", dist)
	}
	newest, newestTime := "", time.Time{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".dar") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest, newestTime = filepath.Join(dist, entry.Name()), info.ModTime()
		}
	}
	if newest == "" {
		return "", fmt.Errorf("daml build produced no DAR in %s", dist)
	}
	return newest, nil
}

// expandUser expands a leading ~ without making the path absolute.
func expandUser(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

func resolve(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path, err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	return absolute, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// shortParty renders Name::first8...last6, matching the readiness line.
func shortParty(value string) string {
	name, fingerprint, found := strings.Cut(value, "::")
	if !found || len(fingerprint) < 14 {
		return value
	}
	return name + "::" + fingerprint[:8] + "..." + fingerprint[len(fingerprint)-6:]
}
