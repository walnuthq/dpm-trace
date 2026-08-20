package integration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/walnuthq/dpm-trace/internal/ledger"
	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/testrunner"
)

// Options configure an integration run.
type Options struct {
	TestDir       string
	Root          string
	Daml          string
	CantonJar     string
	Lit           string
	Parties       string
	DAR           string
	SequencerType string
	CantonTimeout int
	Verbose       bool
	KeepArtifacts bool
	SrcRoot       string // exported to tests as DPM_TRACE_IT_SRC
	Python        string // exported to tests as DPM_TRACE_IT_PYTHON
}

// Run boots a Canton, deploys the DAR, allocates parties, runs lit against the
// live node and tears everything down. Ports run_integration_tests.
//
// The lit exit code is returned unchanged: it is the CI gate.
func Run(stdout, stderr io.Writer, opts Options) (int, error) {
	testDir, err := resolve(opts.TestDir)
	if err != nil {
		return 2, err
	}
	if _, err := os.Stat(testDir); err != nil {
		return 2, fmt.Errorf("integration test dir not found: %s", testDir)
	}

	cantonJar := opts.CantonJar
	if cantonJar == "" {
		cantonJar = os.Getenv("DPM_TRACE_CANTON_JAR")
	}
	if cantonJar != "" {
		cantonJar, _ = resolve(cantonJar)
	}
	if cantonJar == "" {
		return 2, fmt.Errorf("--canton-jar (or DPM_TRACE_CANTON_JAR) must point at a Canton jar")
	}
	if _, err := os.Stat(cantonJar); err != nil {
		return 2, fmt.Errorf("--canton-jar (or DPM_TRACE_CANTON_JAR) must point at a Canton jar")
	}

	daml := expandUser(orDefault(opts.Daml, "daml"))
	lit := orDefault(opts.Lit, "lit")
	placements := ParsePlacements(orDefault(opts.Parties, "Alice,Bob"))
	participants := MaxParticipant(placements)

	// The suite invokes %dpm. Point it at this binary: the generated lit.cfg.py
	// prefers DPM_TRACE_IT_DPM and only falls back to the Python module, so a
	// Go run needs no interpreter at all.
	self, err := os.Executable()
	if err != nil {
		return 2, err
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
	defer func() {
		if opts.KeepArtifacts {
			fmt.Fprintf(stdout, "\nkept Canton log + config: %s\n", work)
			return
		}
		os.RemoveAll(work)
	}()
	logPath := filepath.Join(work, "canton.log")

	ports, err := FreePorts(3 + 3*participants)
	if err != nil {
		return 2, err
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
		return 2, err
	}
	if err := os.WriteFile(bootstrapPath, []byte(BootstrapText(dar, placements, participants)), 0o644); err != nil {
		return 2, err
	}

	endpoints := make([]string, 0, participants)
	for i, p := range participantPorts {
		endpoints = append(endpoints, fmt.Sprintf("participant%d :%d", i+1, p.HTTP))
	}
	fmt.Fprintf(stdout, "booting local Canton (%s); deploying %s ...\n",
		strings.Join(endpoints, ", "), filepath.Base(dar))

	logFile, err := os.Create(logPath)
	if err != nil {
		return 2, err
	}
	canton := exec.Command("java", "-jar", cantonJar, "daemon",
		"-c", configPath, "--bootstrap", bootstrapPath, "--no-tty")
	canton.Stdout = logFile
	canton.Stderr = logFile
	canton.Dir = work
	canton.Env = testrunner.ChildEnv(nil)
	if err := canton.Start(); err != nil {
		logFile.Close()
		return 2, err
	}
	defer func() {
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
	}()

	partyURLs := make([]PartyEndpoint, 0, len(placements))
	for _, placement := range placements {
		partyURLs = append(partyURLs, PartyEndpoint{Name: placement.Name, LedgerURL: participantURLs[placement.Index-1]})
	}
	partyIDs, err := WaitForParties(partyURLs, canton, logPath, opts.CantonTimeout)
	if err != nil {
		return 2, err
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
	fmt.Fprintln(stdout, "Canton ready: "+strings.Join(ready, ", "))

	extra := map[string]string{
		"DPM_TRACE_IT_LEDGER":    participantURLs[0],
		"DPM_TRACE_IT_DAR":       dar,
		"DPM_TRACE_IT_DAML":      daml,
		"DPM_TRACE_IT_DAML_YAML": filepath.Join(opts.Root, "daml.yaml"),
		"DPM_TRACE_IT_DPM":       self,
	}
	// Only forwarded when supplied: a suite pinned to the Python implementation
	// can still opt in, but a Go run does not need an interpreter.
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

	fmt.Fprintf(stdout, "running integration suite: %s\n\n", testDir)
	litFlags := []string{"-v"}
	if opts.Verbose {
		litFlags = []string{"-vv"}
	}
	litCmd := exec.Command(lit, append([]string{testDir}, litFlags...)...)
	litCmd.Env = testrunner.ChildEnv(extra)
	litCmd.Stdout = stdout
	litCmd.Stderr = stderr
	litErr := litCmd.Run()

	code := 0
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
		if opts.Verbose {
			fmt.Fprintf(stderr, "\n--- Canton log (last 60 lines) ---\n%s\n", ReadTail(logPath, 60))
		}
	}
	return code, nil
}

// PartyEndpoint ties a party to the participant that hosts it.
type PartyEndpoint struct {
	Name      string
	LedgerURL string
}

// WaitForParties polls until every party is visible on its participant.
// Ports wait_for_parties.
func WaitForParties(placements []PartyEndpoint, canton *exec.Cmd, logPath string, timeout int) (map[string]string, error) {
	if timeout < 1 {
		timeout = 1
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	found := map[string]string{}
	lastError := ""

	for time.Now().Before(deadline) {
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
			time.Sleep(3 * time.Second)
			return found, nil
		}
		time.Sleep(time.Second)
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
