package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/walnuthq/dpm-trace/internal/ledger"
	"github.com/walnuthq/dpm-trace/internal/model"
)

const (
	partyPollInterval = 500 * time.Millisecond
	uploadRetries     = 3
	requestTimeout    = 2 * time.Minute
	revokeTimeout     = 10 * time.Second
	grantTimeout      = 30 * time.Second
)

func redactUserinfo(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	prefix := ""
	if scheme := strings.Index(raw, "://"); scheme >= 0 && scheme < at {
		prefix = raw[:scheme+len("://")]
	} else if strings.HasPrefix(raw, "//") {
		prefix = "//"
	}
	return prefix + "REDACTED" + raw[at:]
}

// ValidateAttach rejects option combinations that make no sense for an
// attach run, and a --token-file given when there is nothing to attach to. It
// also checks that every token file is readable and not empty, so a typo fails
// before the DAR is built. Every --ledger-url must be an http or https URL
// with a hostname and without embedded credentials; errors never echo
// credentials from a URL. A query or fragment is rejected without being
// echoed, because API paths are appended to the URL textually.
func ValidateAttach(opts Options, participants int) error {
	if len(opts.LedgerURLs) == 0 {
		if len(opts.TokenFiles) > 0 {
			return errors.New("--token-file authenticates --ledger-url participants and cannot be used without --ledger-url")
		}
		return nil
	}
	if opts.CantonJar != "" || opts.CantonJarGiven {
		return errors.New("--ledger-url attaches to an existing participant and cannot be combined with --canton-jar")
	}
	if len(opts.LedgerURLs) != participants {
		return fmt.Errorf("--ledger-url given %d times but --parties places parties on %d participant(s); pass one --ledger-url per participant, in order",
			len(opts.LedgerURLs), participants)
	}
	for _, ledgerURL := range opts.LedgerURLs {
		if strings.ContainsAny(ledgerURL, "?#") {
			return errors.New("--ledger-url must not have a query (?) or a fragment (#): the API paths are appended to it as text; pass the base URL only")
		}
		parsed, err := url.Parse(ledgerURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return fmt.Errorf("--ledger-url %q must be an http or https URL with a hostname", redactUserinfo(ledgerURL))
		}
		if parsed.User != nil {
			return errors.New("--ledger-url must not contain credentials (user:password@); pass a token with --token-file")
		}
	}
	if len(opts.TokenFiles) != 0 && len(opts.TokenFiles) != len(opts.LedgerURLs) {
		return fmt.Errorf("--token-file given %d times for %d --ledger-url value(s); pass exactly one --token-file per --ledger-url, in order, or none",
			len(opts.TokenFiles), len(opts.LedgerURLs))
	}
	for _, tokenFile := range opts.TokenFiles {
		if token, err := (Participant{TokenFile: tokenFile}).token(); err != nil {
			return fmt.Errorf("--token-file %s: %w", tokenFile, err)
		} else if token == "" {
			return fmt.Errorf("--token-file %s is empty", tokenFile)
		}
	}
	return nil
}

// Participant is an existing participant reached over the JSON Ledger API. A
// non-empty TokenFile is re-read on every request, because identity providers
// such as Keycloak issue tokens that expire within minutes and the file is
// expected to be refreshed during a long run.
type Participant struct {
	URL       string
	TokenFile string
	Timeout   time.Duration
}

func (p Participant) token() (string, error) {
	if p.TokenFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (p Participant) request(ctx context.Context, method, path, contentType string, body []byte) (*model.Object, error) {
	token, err := p.token()
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	endpoint := ledger.JoinURL(p.URL, path)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = requestTimeout
	}
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w", method, endpoint, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &ledger.HTTPError{Method: method, URL: endpoint, StatusCode: resp.StatusCode, Detail: string(data)}
	}
	if readErr != nil {
		return nil, fmt.Errorf("%s %s: read response: %w", method, endpoint, readErr)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return model.Decode(data)
}

func (p Participant) requestJSON(ctx context.Context, method, path string, payload map[string]any) (*model.Object, error) {
	encoded, err := model.Encode(payload)
	if err != nil {
		return nil, err
	}
	value, err := p.request(ctx, method, path, "application/json", encoded)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return model.NewObject(), nil
	}
	return value, nil
}

// UploadDAR posts the DAR bytes to /v2/packages. Uploads are idempotent, so
// network errors and 5xx responses are retried, at most three attempts in all
// with a growing pause, and cancelling ctx stops the retries. A package whose name
// and version already exist with different content is reported with advice to
// bump the version, instead of Canton's raw error.
func (p Participant) UploadDAR(ctx context.Context, dar []byte) error {
	var err error
	for attempt := 0; attempt < uploadRetries; attempt++ {
		if _, err = p.request(ctx, "POST", "/v2/packages", "application/octet-stream", dar); err == nil {
			return nil
		}
		var httpErr *ledger.HTTPError
		if errors.As(err, &httpErr) {
			if strings.Contains(httpErr.Detail, "KNOWN_PACKAGE_VERSION") || strings.Contains(httpErr.Detail, "KNOWN_DAR_VERSION") {
				return fmt.Errorf("%s already has a package with this name and version but different content; bump `version` in daml.yaml and rebuild", p.URL)
			}
			if httpErr.StatusCode < 500 {
				return err
			}
		}
		if ctx.Err() != nil || attempt == uploadRetries-1 {
			break
		}
		if sleepErr := sleep(ctx, time.Duration(attempt+1)*time.Second); sleepErr != nil {
			return sleepErr
		}
	}
	return err
}

// AllocateParty creates a party with the given id hint and returns its full id.
func (p Participant) AllocateParty(ctx context.Context, hint string) (string, error) {
	resp, err := p.requestJSON(ctx, "POST", "/v2/parties", map[string]any{"partyIdHint": hint, "identityProviderId": ""})
	if err != nil {
		return "", err
	}
	details, _ := resp.Get("partyDetails")
	detail, _ := details.(*model.Object)
	party := model.ObjectString(detail, "party")
	if party == "" {
		return "", fmt.Errorf("POST /v2/parties returned no party for hint %q", hint)
	}
	return party, nil
}

// AuthenticatedUser returns the id of the user the participant maps the token to.
func (p Participant) AuthenticatedUser(ctx context.Context) (string, error) {
	resp, err := p.request(ctx, "GET", "/v2/authenticated-user", "", nil)
	if err != nil {
		return "", err
	}
	if resp != nil {
		if raw, ok := resp.Get("user"); ok {
			if user, isObject := raw.(*model.Object); isObject {
				if id := model.ObjectString(user, "id"); id != "" {
					return id, nil
				}
			}
		}
	}
	return "", fmt.Errorf("GET /v2/authenticated-user on %s returned no user id", p.URL)
}

// Grant is a set of rights given to a user on one participant.
type Grant struct {
	Participant Participant
	UserID      string
	ActAs       []string
	ReadAs      []string
}

func (g Grant) rights() []any {
	right := func(kind, party string) any {
		return map[string]any{"kind": map[string]any{kind: map[string]any{"value": map[string]any{"party": party}}}}
	}
	var rights []any
	for _, party := range g.ActAs {
		rights = append(rights, right("CanActAs", party))
	}
	for _, party := range g.ReadAs {
		rights = append(rights, right("CanReadAs", party))
	}
	return rights
}

func (g Grant) send(ctx context.Context, method string) error {
	if len(g.rights()) == 0 {
		return nil
	}
	_, err := g.Participant.requestJSON(ctx, method, "/v2/users/"+url.PathEscape(g.UserID)+"/rights", map[string]any{
		"userId":             g.UserID,
		"identityProviderId": "",
		"rights":             g.rights(),
	})
	return err
}

type recordedGrant struct {
	grant     Grant
	unsettled error
}

// Grants records the rights an attach run has given, so they can be revoked.
type Grants struct {
	given []recordedGrant
}

func outcomeIsKnown(err error) bool {
	var httpErr *ledger.HTTPError
	return err == nil || (errors.As(err, &httpErr) && httpErr.StatusCode < http.StatusInternalServerError)
}

// Give records g for Revoke and then grants it on its participant. It records
// first, so a grant whose response is lost is still revoked: revoking a right
// the user never held succeeds. Once the request is issued it is not cancelled
// with ctx, only bounded by a timeout. A grant that is answered, or refused
// with a 4xx status, has a known outcome. One that times out or ends without
// a usable answer may still be applied by the participant after the revoke has
// run, so Revoke warns about it. A ctx that is already done issues nothing.
func (gs *Grants) Give(ctx context.Context, g Grant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	issued, cancel := context.WithTimeout(context.WithoutCancel(ctx), grantTimeout)
	defer cancel()
	record := recordedGrant{grant: g}
	index := len(gs.given)
	gs.given = append(gs.given, record)
	err := g.send(issued, "POST")
	if !outcomeIsKnown(err) {
		gs.given[index].unsettled = err
	}
	return err
}

// Revoke withdraws every recorded grant, newest first, with each request
// bounded by a short timeout so teardown cannot hang. A grant that ended
// without a known outcome is first reported to log as possibly still granted,
// naming the participant, user and parties, because the participant may apply
// it after the revoke. Revoking is best effort: failures are reported to log,
// naming the user and parties to clean up, and do not stop the remaining
// revocations. It is safe on a nil receiver.
func (gs *Grants) Revoke(log io.Writer) {
	if gs == nil {
		return
	}
	for i := len(gs.given) - 1; i >= 0; i-- {
		g := gs.given[i].grant
		if unsettled := gs.given[i].unsettled; unsettled != nil {
			fmt.Fprintf(log, "warning: the grant of rights to user %s on %s (CanActAs %v, CanReadAs %v) ended without a confirmed answer and may still be in place: %v\n",
				g.UserID, g.Participant.URL, g.ActAs, g.ReadAs, unsettled)
		}
		g.Participant.Timeout = revokeTimeout
		if err := g.send(context.Background(), "PATCH"); err != nil {
			fmt.Fprintf(log, "warning: could not revoke rights of user %s on %s (CanActAs %v, CanReadAs %v); revoke them manually: %v\n",
				g.UserID, g.Participant.URL, g.ActAs, g.ReadAs, err)
		}
	}
	gs.given = nil
}

func listsParty(resp *model.Object, party string) bool {
	if resp == nil {
		return false
	}
	entries, _ := model.ObjectValue(resp, "partyDetails").([]any)
	for _, entry := range entries {
		if object, ok := entry.(*model.Object); ok && model.ObjectString(object, "party") == party {
			return true
		}
	}
	return false
}

// WaitForParty polls until the participant knows party, the timeout elapses
// or ctx is cancelled. The timeout is one deadline for the whole wait,
// applied to every request and pause, and a ctx with an earlier deadline wins.
// Each poll is one lookup, so the cost does not grow with the number of
// parties on the network.
func (p Participant) WaitForParty(ctx context.Context, party string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastError := "party not visible yet"
	expired := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("party %s not visible on %s within %s (last error: %s)", party, p.URL, timeout, lastError)
		}
		return ctx.Err()
	}
	for {
		resp, err := p.request(ctx, "GET", "/v2/parties/"+url.PathEscape(party), "", nil)
		if err == nil && listsParty(resp, party) {
			return nil
		}
		var httpErr *ledger.HTTPError
		if err != nil && (!errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound) {
			lastError = err.Error()
		}
		if ctx.Err() != nil {
			return expired()
		}
		if sleep(ctx, partyPollInterval) != nil {
			return expired()
		}
	}
}

// RunSuffix returns a short random suffix that keeps party hints unique
// across runs on a long-lived network.
func RunSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffffff, 16)
	}
	return hex.EncodeToString(buf)
}

// Attach uploads the DAR to every participant, allocates each placement's
// party with a per-run hint suffix and waits until every participant sees every
// party. Each participant that has a token file then gets its user CanActAs on
// the parties placed on it and CanReadAs on the others, so tests can read as a
// party hosted elsewhere. A participant without a token file is
// unauthenticated: it has no user, so it gets no grants. Cancelling ctx stops
// setup. The returned map is keyed by the logical name. The returned Grants is
// never nil, even on error, and holds whatever was granted so far.
func Attach(ctx context.Context, log io.Writer, participants []Participant, dar []byte, placements []Placement, suffix string, timeout time.Duration) (map[string]string, *Grants, error) {
	grants := &Grants{}
	for i, participant := range participants {
		fmt.Fprintf(log, "uploading DAR to participant%d ...\n", i+1)
		if err := participant.UploadDAR(ctx, dar); err != nil {
			return nil, grants, fmt.Errorf("participant%d: upload DAR: %w", i+1, err)
		}
	}

	ids := map[string]string{}
	local := make([][]string, len(participants))
	for _, placement := range placements {
		participant := participants[placement.Index-1]
		party, err := participant.AllocateParty(ctx, placement.Name+"-"+suffix)
		if err != nil {
			return nil, grants, fmt.Errorf("participant%d: allocate %s: %w", placement.Index, placement.Name, err)
		}
		ids[placement.Name] = party
		local[placement.Index-1] = append(local[placement.Index-1], party)
	}
	for _, party := range ids {
		if err := waitForPartyEverywhere(ctx, participants, party, timeout); err != nil {
			return nil, grants, err
		}
	}

	for i, participant := range participants {
		if participant.TokenFile == "" {
			continue
		}
		userID, err := participant.AuthenticatedUser(ctx)
		if err != nil {
			return nil, grants, fmt.Errorf("participant%d: %w", i+1, err)
		}
		var remote []string
		for j, parties := range local {
			if j != i {
				remote = append(remote, parties...)
			}
		}
		if err := grants.Give(ctx, Grant{Participant: participant, UserID: userID, ActAs: local[i], ReadAs: remote}); err != nil {
			return nil, grants, fmt.Errorf("participant%d: grant rights: %w", i+1, err)
		}
	}
	return ids, grants, nil
}

func waitForPartyEverywhere(ctx context.Context, participants []Participant, party string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, participant := range participants {
		if err := participant.WaitForParty(ctx, party, timeout); err != nil {
			return err
		}
	}
	return nil
}

var litConfigPrecedence = []string{"lit.site.cfg.py", "lit.site.cfg", "lit.cfg.py", "lit.cfg"}

const runnerConfigForwarding = `if os.environ.get("DPM_TRACE_CONFIG"):
    config.environment["DPM_TRACE_CONFIG"] = os.environ["DPM_TRACE_CONFIG"]`

// CheckSuiteForwardsRunnerConfig is a best-effort guard for an attach run: it
// verifies that the lit configuration lit would load for the suite passes
// DPM_TRACE_CONFIG on to the test commands. lit clears the environment, so a
// suite that does not forward it lets the project's .dpm-trace.json, token
// included, reach the commands the runner promised to isolate. Suites
// scaffolded before attach mode existed are in that state, and --init keeps
// existing files.
//
// lit looks for its configuration in the test directory and then in each
// ancestor, and in a directory it executes one file only, chosen by lit's
// precedence: lit.site.cfg.py, lit.site.cfg, lit.cfg.py, lit.cfg. The guard
// reads that file and requires an uncommented mention of DPM_TRACE_CONFIG in
// it. It does not run the configuration, so a mention that is never executed,
// or a forward done by a file the configuration loads, is not understood. A
// tree without any lit configuration cannot be isolated and is refused. lit
// also loads the configuration of nested suites below the test directory; the
// guard does not read those, so each must forward DPM_TRACE_CONFIG itself.
func CheckSuiteForwardsRunnerConfig(testDir string) error {
	path, err := litConfigLitWouldLoad(testDir)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if forwardsRunnerConfig(string(data)) {
		return nil
	}
	return fmt.Errorf("%s, the suite's own lit configuration, does not forward DPM_TRACE_CONFIG to the test commands, so in attach mode the project's .dpm-trace.json (token and ledger URL included) would reach them. This check is a best-effort guard that looks for an uncommented mention of the variable in that one file. It does not read nested suites: every nested suite with its own lit configuration must forward DPM_TRACE_CONFIG too.\n"+
		"Add these lines to it, after the other config.environment settings, or copy the current lit.cfg.py that `dpm trace test --init` generates:\n\n%s",
		path, runnerConfigForwarding)
}

func litConfigLitWouldLoad(testDir string) (string, error) {
	dir, err := filepath.Abs(testDir)
	if err != nil {
		return "", err
	}
	for {
		for _, name := range litConfigPrecedence {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no lit configuration (%s) found in %s or any parent directory, so attach mode cannot isolate the project's .dpm-trace.json from the test commands; add the lit.cfg.py that `dpm trace test --init` generates",
				strings.Join(litConfigPrecedence, ", "), testDir)
		}
		dir = parent
	}
}

func forwardsRunnerConfig(litConfig string) bool {
	for _, line := range strings.Split(litConfig, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "DPM_TRACE_CONFIG") {
			return true
		}
	}
	return false
}

var reservedPartyExportName = regexp.MustCompile(`^(LEDGER\d*|TOKEN_FILE\d*|DAR|DAML|DAML_YAML|DPM|PYTHON|SRC)$`)

// RejectReservedPartyNames fails when a party's exported variable,
// DPM_TRACE_IT_ plus its upper-cased name, would be one the runner sets for
// something else, such as the ledger URL or a token file. Left alone, the party
// id would silently replace that value in the suite's environment.
func RejectReservedPartyNames(placements []Placement) error {
	for _, placement := range placements {
		exported := strings.ToUpper(placement.Name)
		if reservedPartyExportName.MatchString(exported) {
			return fmt.Errorf("party %q would be exported as DPM_TRACE_IT_%s, which the runner uses for something else; rename the party", placement.Name, exported)
		}
	}
	return nil
}
