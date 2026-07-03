package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

var (
	pushTo       []string
	pushCDP      []string
	pushVia      string
	pushDomains  []string
	pushBrowser  string
	pushSkipDBSC bool
	pushDryRun   bool
)

const pushCDPTimeout = 25 * time.Second

var pushCmd = &cobra.Command{
	Use:   "push [target]",
	Short: "Push this machine's live cookies into daemonless targets",
	Long: `push runs the same live read pipeline as export and agent-sync, then
writes the surviving cookies into one or more destinations. It is intentionally
daemonless: for CDP-over-SSH targets, the remote machine only needs SSH and a
reachable browser DevTools endpoint; it does not need agentcookie installed.

Named targets live in targets.yaml next to source.yaml:

  version: 1
  targets:
    kernel-browser:
      via: ssh://aircover1.ws
      to:
        - cdp://127.0.0.1:19222

Equivalent one-off form:

  agentcookie push --via ssh://aircover1.ws --to cdp://127.0.0.1:19222
  agentcookie push --via ssh://aircover1.ws --cdp 127.0.0.1:19222
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPush,
}

func init() {
	pushCmd.Flags().StringArrayVar(&pushTo, "to", nil, "destination URI (repeatable), e.g. cdp://127.0.0.1:19222")
	pushCmd.Flags().StringArrayVar(&pushCDP, "cdp", nil, "CDP endpoint convenience alias for --to cdp://HOST:PORT (repeatable)")
	pushCmd.Flags().StringVar(&pushVia, "via", "", "connector URI, e.g. ssh://aircover1.ws; omitted means local")
	pushCmd.Flags().StringSliceVar(&pushDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	pushCmd.Flags().StringVar(&pushBrowser, "browser", "", "source browser name (default: source.yaml browser, then Chrome)")
	pushCmd.Flags().BoolVar(&pushSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	pushCmd.Flags().BoolVar(&pushDryRun, "dry-run", false, "resolve the target and read cookies, but do not write to destinations")
}

type pushPlan struct {
	Name string
	Via  string
	To   []string
}

type pushResult struct {
	Target      string `json:"target,omitempty"`
	Via         string `json:"via,omitempty"`
	Destination string `json:"destination"`
	Cookies     int    `json:"cookies"`
	Contexts    int    `json:"contexts,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	DBSCWarned  int    `json:"dbsc_warned"`
	DBSCSkipped int    `json:"dbsc_skipped"`
	Policy      string `json:"policy"`
}

func runPush(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	plan, err := resolvePushPlan(args)
	if err != nil {
		return err
	}

	cookies, st, policy, err := readPushCookies()
	if err != nil {
		return err
	}

	if pushDryRun {
		return emitPushResults(cmd, []pushResult{{
			Target:      plan.Name,
			Via:         plan.Via,
			Destination: strings.Join(plan.To, ","),
			Cookies:     len(cookies),
			DryRun:      true,
			DBSCWarned:  st.dbsc.warned,
			DBSCSkipped: st.dbsc.skipped,
			Policy:      policy,
		}})
	}

	results := make([]pushResult, 0, len(plan.To))
	for _, dest := range plan.To {
		contexts, err := pushToDestination(ctx, plan.Via, dest, cookies)
		if err != nil {
			return err
		}
		results = append(results, pushResult{
			Target:      plan.Name,
			Via:         plan.Via,
			Destination: dest,
			Cookies:     len(cookies),
			Contexts:    contexts,
			DBSCWarned:  st.dbsc.warned,
			DBSCSkipped: st.dbsc.skipped,
			Policy:      policy,
		})
	}
	return emitPushResults(cmd, results)
}

func resolvePushPlan(args []string) (pushPlan, error) {
	var plan pushPlan
	if len(args) == 1 {
		plan.Name = args[0]
		targets, err := config.LoadTargets(common.ConfigDir)
		if err != nil {
			return plan, err
		}
		target, ok := targets.Targets[plan.Name]
		if !ok {
			return plan, fmt.Errorf("push target %q not found in %s", plan.Name, "targets.yaml")
		}
		plan.Via = target.Via
		plan.To = append([]string(nil), target.To...)
	}
	if pushVia != "" {
		plan.Via = pushVia
	}
	if len(pushTo) > 0 || len(pushCDP) > 0 {
		plan.To = nil
		plan.To = append(plan.To, pushTo...)
		for _, cdp := range pushCDP {
			plan.To = append(plan.To, normalizeCDPAlias(cdp))
		}
	}
	if len(plan.To) == 0 {
		return plan, fmt.Errorf("push requires a target name or at least one --to/--cdp destination")
	}
	return plan, nil
}

func normalizeCDPAlias(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	return "cdp://" + raw
}

func readPushCookies() ([]chrome.Cookie, readStats, string, error) {
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return nil, readStats{}, "", err
	}
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return nil, readStats{}, "", err
	}
	browserName := pushBrowser
	if browserName == "" {
		browserName = cfg.Browser.Name
	}
	sourceBrowser, err := chrome.LookupBrowser(browserName)
	if err != nil {
		return nil, readStats{}, "", err
	}
	password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
	if err != nil {
		return nil, readStats{}, "", err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return nil, readStats{}, "", err
	}
	skipDBSC := pushSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"
	cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
	if err != nil {
		return nil, readStats{}, "", err
	}
	cookies = sinkpush.FilterByHostPatterns(cookies, pushDomains)
	return cookies, st, blocklist.CookiePolicySummary(), nil
}

func pushToDestination(ctx context.Context, via, dest string, cookies []chrome.Cookie) (int, error) {
	endpoint, err := cdpEndpointFromDestination(dest)
	if err != nil {
		return 0, err
	}
	cleanup := func() {}
	if via != "" {
		endpoint, cleanup, err = openSSHTunnel(ctx, via, endpoint)
		if err != nil {
			return 0, err
		}
	}
	defer cleanup()
	return injectCDPEndpoint(ctx, endpoint, cookies)
}

func cdpEndpointFromDestination(dest string) (string, error) {
	u, err := url.Parse(dest)
	if err != nil {
		return "", fmt.Errorf("parse destination %q: %w", dest, err)
	}
	switch u.Scheme {
	case "cdp":
		if u.Hostname() == "" || u.Port() == "" {
			return "", fmt.Errorf("cdp destination %q must include host and port", dest)
		}
		return "http://" + u.Host, nil
	case "http", "https":
		return strings.TrimRight(dest, "/"), nil
	default:
		return "", fmt.Errorf("unsupported push destination %q (supported: cdp://, http://, https://)", dest)
	}
}

func injectCDPEndpoint(ctx context.Context, endpoint string, cookies []chrome.Cookie) (int, error) {
	if err := waitForPushCDP(ctx, endpoint, pushCDPTimeout); err != nil {
		return 0, err
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, endpoint)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()
	contexts, err := livecdp.InjectBrowserContexts(browserCtx, cookies)
	if err != nil {
		return contexts, fmt.Errorf("push cdp %s: %w", endpoint, err)
	}
	return contexts, nil
}

func waitForPushCDP(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(strings.TrimRight(endpoint, "/") + "/json/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("cdp endpoint %s not reachable within %s", endpoint, timeout)
}

func openSSHTunnel(ctx context.Context, via, endpoint string) (string, func(), error) {
	sshTarget, sshPort, err := parseSSHVia(via)
	if err != nil {
		return "", nil, err
	}
	remote, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, fmt.Errorf("parse cdp endpoint %q: %w", endpoint, err)
	}
	if remote.Hostname() == "" || remote.Port() == "" {
		return "", nil, fmt.Errorf("cdp endpoint %q must include host and port for ssh forwarding", endpoint)
	}
	localPort, err := reserveLocalPort()
	if err != nil {
		return "", nil, err
	}

	forward := fmt.Sprintf("127.0.0.1:%d:%s:%s", localPort, remote.Hostname(), remote.Port())
	args := sshTunnelArgs(sshTarget, sshPort, forward)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start ssh tunnel via %s: %w", via, err)
	}
	cleanup := func() { stopTunnel(cmd) }
	localEndpoint := fmt.Sprintf("%s://127.0.0.1:%d", remote.Scheme, localPort)
	if err := waitForPushCDP(ctx, localEndpoint, pushCDPTimeout); err != nil {
		cleanup()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", nil, fmt.Errorf("ssh tunnel via %s failed: %w: %s", via, err, msg)
		}
		return "", nil, fmt.Errorf("ssh tunnel via %s failed: %w", via, err)
	}
	return localEndpoint, cleanup, nil
}

func parseSSHVia(via string) (target string, port string, err error) {
	u, err := url.Parse(via)
	if err != nil {
		return "", "", fmt.Errorf("parse via %q: %w", via, err)
	}
	if u.Scheme != "ssh" {
		return "", "", fmt.Errorf("unsupported connector %q (supported: ssh://)", via)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("ssh connector %q must include a host", via)
	}
	if _, ok := u.User.Password(); ok {
		return "", "", fmt.Errorf("ssh connector %q must not include a password; use ssh-agent or ~/.ssh/config", via)
	}
	target = u.Hostname()
	if user := u.User.Username(); user != "" {
		target = user + "@" + target
	}
	return target, u.Port(), nil
}

func sshTunnelArgs(target, port, forward string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-N", "-L", forward}
	if port != "" {
		args = append(args, "-p", port)
	}
	return append(args, target)
}

func reserveLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve local port: %w", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve local port: unexpected addr %T", ln.Addr())
	}
	return addr.Port, nil
}

func stopTunnel(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func emitPushResults(cmd *cobra.Command, results []pushResult) error {
	if common.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
	}
	for _, r := range results {
		dbsc := dbscSummary{warned: r.DBSCWarned, skipped: r.DBSCSkipped}
		if r.DryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "agentcookie push: %d cookies after cookie policy (%s), destination=%s via=%s (dry-run=true)%s\n", r.Cookies, r.Policy, r.Destination, displayVia(r.Via), dbscNote(dbsc))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "agentcookie push: injected %d cookies into %d CDP context(s), destination=%s via=%s%s\n", r.Cookies, r.Contexts, r.Destination, displayVia(r.Via), dbscNote(dbsc))
	}
	return nil
}

func displayVia(via string) string {
	if via == "" {
		return "local"
	}
	return via
}
