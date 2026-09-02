package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	"github.com/spf13/cobra"
)

// vecgrep doctor answers, in one run, the question that otherwise costs a
// debugging session: "why does vecgrep say the provider has no API key when I
// exported it?" It reports what THIS process can see — the resolved provider,
// where its key comes from (never the value), whether the index store exists,
// whether the embedder answers — so the report is true for whichever launcher
// (terminal, mcphub, Claude Code, Codex) started it.

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose provider credentials, index state, and the launcher environment",
	Long: `Diagnose the vecgrep setup as seen by this process.

Checks the resolved project and config sources, the embedding provider and
where its API key comes from (environment variable name or config field — the
value is never printed), local Ollama reachability, the index store, the
background daemon, and — unless --no-ping is given — one live embedding
request against the provider.

Run it from the same launcher that reports a problem: an MCP gateway or a
GUI-launched agent does not inherit your interactive shell, so a key that is
set in your terminal can still be missing there.`,
	RunE:         runDoctor,
	SilenceUsage: true,
}

const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
	doctorInfo = "info"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

type doctorReport struct {
	Version string        `json:"version"`
	Checks  []doctorCheck `json:"checks"`
	Failed  int           `json:"failed"`
	Warned  int           `json:"warned"`
}

func (r *doctorReport) add(name, status, detail, hint string) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Status: status, Detail: detail, Hint: hint})
	switch status {
	case doctorFail:
		r.Failed++
	case doctorWarn:
		r.Warned++
	}
}

type doctorOptions struct {
	// StartDir is the directory to resolve the project from ("" = cwd).
	StartDir string
	// Ping enables the live embedding request.
	Ping bool
	// PingTimeout bounds that request.
	PingTimeout time.Duration
	// ProbeTimeout bounds the local Ollama reachability probe.
	ProbeTimeout time.Duration
}

func runDoctor(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	noPing, _ := cmd.Flags().GetBool("no-ping")

	report := collectDoctorReport(cmd.Context(), doctorOptions{
		Ping:         !noPing,
		PingTimeout:  15 * time.Second,
		ProbeTimeout: 2 * time.Second,
	})

	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	} else {
		printDoctorReport(os.Stdout, report)
	}
	if report.Failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", report.Failed)
	}
	return nil
}

// collectDoctorReport runs every check. It never opens VecLite (index facts
// come from the vector-free health manifest) so it is safe to run beside a
// live daemon or MCP server.
func collectDoctorReport(ctx context.Context, opts doctorOptions) doctorReport {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = 2 * time.Second
	}
	if opts.PingTimeout <= 0 {
		opts.PingTimeout = 15 * time.Second
	}
	report := doctorReport{Version: version.Version}
	report.add("version", doctorInfo, fmt.Sprintf("vecgrep %s (%s)", version.Version, version.Commit), "")

	cfg, projectRoot, sources := doctorResolveConfig(&report, opts.StartDir)

	// Provider + key origin.
	key := app.ResolveProviderKeyStatus(cfg)
	report.add("provider", doctorInfo,
		fmt.Sprintf("%s / %s (%d dimensions)", key.Provider, cfg.Embedding.Model, cfg.Embedding.Dimensions),
		fmt.Sprintf("config sources: %s", strings.Join(sources, ", ")))
	if key.RequiresKey {
		if key.Missing() {
			report.add("api-key", doctorFail,
				fmt.Sprintf("no API key visible to this process (checked env %s and config %s)",
					strings.Join(embed.APIKeyEnvVars(key.Provider), ", "), embed.APIKeyConfigField(key.Provider)),
				embed.LauncherEnvHint)
		} else {
			report.add("api-key", doctorOK, "from "+key.Source, "")
		}
	}
	if other := otherProviderKeysInEnv(key.Provider); len(other) > 0 {
		report.add("other-keys", doctorInfo,
			"API keys for other providers are visible: "+strings.Join(other, ", "),
			"switch with `vecgrep config set embedding.provider <name>` if that was the intent")
	}

	// Local Ollama: the configured backend, or the keyless fallback.
	ollamaURL := cfg.Embedding.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, opts.ProbeTimeout)
	detected := embed.DetectProviders(probeCtx, embed.DetectConfig{
		OllamaURL:      ollamaURL,
		PreferredModel: cfg.Embedding.Model,
		Timeout:        opts.ProbeTimeout,
	})
	cancelProbe()
	ollamaUp := false
	for _, d := range detected {
		if d.Type == embed.ProviderOllama {
			ollamaUp = d.Available
		}
	}
	switch {
	case key.Provider == embed.ProviderOllama && ollamaUp:
		report.add("ollama", doctorOK, "reachable at "+ollamaURL, "")
	case key.Provider == embed.ProviderOllama:
		report.add("ollama", doctorFail, "not reachable at "+ollamaURL,
			"start it with `ollama serve` and pull the model with `ollama pull "+cfg.Embedding.Model+"`")
	case ollamaUp:
		hint := ""
		if key.Missing() {
			hintCtx, cancel := context.WithTimeout(ctx, opts.ProbeTimeout)
			hint = embed.LocalOllamaHint(hintCtx)
			cancel()
		}
		report.add("ollama", doctorInfo, "a local ollama is reachable at "+ollamaURL+" (not the configured provider)", hint)
	}

	// Live provider probe.
	switch {
	case !opts.Ping:
		report.add("provider-ping", doctorInfo, "skipped (--no-ping)", "")
	case key.Missing():
		report.add("provider-ping", doctorInfo, "skipped: no API key", "")
	case key.Provider == embed.ProviderOllama && !ollamaUp:
		report.add("provider-ping", doctorInfo, "skipped: ollama unreachable", "")
	default:
		provider, err := app.NewProvider(cfg)
		if err != nil {
			report.add("provider-ping", doctorFail, err.Error(), "")
			break
		}
		pingCtx, cancel := context.WithTimeout(ctx, opts.PingTimeout)
		err = provider.Ping(pingCtx)
		cancel()
		if closer, ok := provider.(interface{ Close() }); ok {
			closer.Close()
		}
		if err != nil {
			report.add("provider-ping", doctorFail, err.Error(), "the key is present but the provider rejected the request; check validity, credits, and base URL")
		} else {
			report.add("provider-ping", doctorOK, "embedding request succeeded", "")
		}
	}

	// Index store.
	if projectRoot != "" {
		vecPath := db.VecLitePath(cfg.DataDir)
		info, err := os.Stat(vecPath)
		switch {
		case err != nil:
			hint := "run `vecgrep index`"
			if key.Missing() {
				hint += " after fixing the API key"
			}
			report.add("index", doctorWarn, "not built: "+vecPath+" does not exist", hint)
		default:
			detail := fmt.Sprintf("%s (%s)", vecPath, formatBytes(info.Size()))
			if status, lErr := app.LightweightStatus(ctx, opts.StartDir); lErr == nil && status != nil {
				detail += fmt.Sprintf("; files %d, chunks %d", status.Stats["files"], status.Stats["chunks"])
				if status.Freshness != nil {
					detail += fmt.Sprintf(", freshness %s", status.Freshness.State)
				}
			}
			report.add("index", doctorOK, detail, "")
		}
	}

	// Background daemon.
	if dir, err := config.GetGlobalConfigDir(); err == nil {
		doctorDaemon(&report, filepath.Join(dir, "daemon.json"))
	}

	return report
}

// doctorResolveConfig resolves the project (if any) and returns the effective
// config. Outside a project it falls back to the global defaults so provider
// and key checks still describe what `vecgrep init` would use.
func doctorResolveConfig(report *doctorReport, startDir string) (*config.Config, string, []string) {
	if startDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			startDir = cwd
		}
	}
	absStart, _ := filepath.Abs(startDir)
	projectRoot, err := config.FindProjectRootFrom(absStart)
	if err == nil {
		resolver := config.NewConfigResolution()
		if resolved, rErr := resolver.Resolve(projectRoot); rErr == nil {
			detail := projectRoot
			if resolved.ProjectName != "" {
				detail = fmt.Sprintf("%s (%s)", resolved.ProjectName, projectRoot)
			}
			if resolved.Branch != "" {
				detail += ", branch " + resolved.Branch
			}
			report.add("project", doctorOK, detail, "")
			sources := resolver.FoundConfigFiles()
			if len(sources) == 0 {
				sources = []string{"no project config files; global defaults (~/.vecgrep/config.yaml) and built-in defaults"}
			}
			return resolved.Config, projectRoot, sources
		}
		report.add("project", doctorWarn, "config resolution failed for "+projectRoot, "")
	} else {
		report.add("project", doctorWarn, "not inside a vecgrep project ("+absStart+")", "run `vecgrep init` in the project root, or cd into a registered project")
	}
	cfg := config.DefaultConfig()
	sources := []string{"built-in defaults"}
	if global, gErr := config.LoadGlobalConfig(); gErr == nil && global != nil {
		if global.Defaults.Embedding.Provider != "" {
			cfg.Embedding = global.Defaults.Embedding
			sources = []string{"global defaults (~/.vecgrep/config.yaml)"}
		}
	}
	return cfg, "", sources
}

// otherProviderKeysInEnv lists env variables holding keys for cloud providers
// other than the configured one. A key for the wrong provider is the classic
// "but I set it!" mismatch.
func otherProviderKeysInEnv(configured embed.ProviderType) []string {
	var out []string
	for _, p := range []embed.ProviderType{embed.ProviderOpenAI, embed.ProviderCohere, embed.ProviderVoyage} {
		if p == configured {
			continue
		}
		for _, name := range embed.APIKeyEnvVars(p) {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func doctorDaemon(report *doctorReport, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		report.add("daemon", doctorInfo, "not running (no daemon.json)", "")
		return
	}
	var meta struct {
		PID      int      `json:"pid"`
		Projects []string `json:"projects"`
	}
	if err := json.Unmarshal(data, &meta); err != nil || meta.PID <= 0 {
		report.add("daemon", doctorWarn, "daemon.json is unreadable: "+path, "remove it and run `vecgrep daemon start` if you use the daemon")
		return
	}
	if processAlive(meta.PID) {
		report.add("daemon", doctorInfo, fmt.Sprintf("running (pid %d, %d project(s))", meta.PID, len(meta.Projects)), "")
		return
	}
	report.add("daemon", doctorWarn, fmt.Sprintf("stale daemon.json: pid %d is not running", meta.PID),
		"run `vecgrep daemon stop` (or delete "+path+") so tools stop trying the socket")
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func printDoctorReport(w io.Writer, report doctorReport) {
	fmt.Fprintln(w, "vecgrep doctor")
	fmt.Fprintln(w, "--------------")
	for _, c := range report.Checks {
		fmt.Fprintf(w, "%s  %-14s %s\n", doctorGlyph(c.Status), c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(w, "   %-14s %s\n", "", c.Hint)
		}
	}
	fmt.Fprintln(w)
	switch {
	case report.Failed > 0:
		fmt.Fprintf(w, "%d check(s) failed, %d warning(s).\n", report.Failed, report.Warned)
	case report.Warned > 0:
		fmt.Fprintf(w, "All checks passed with %d warning(s).\n", report.Warned)
	default:
		fmt.Fprintln(w, "All checks passed.")
	}
}

func doctorGlyph(status string) string {
	switch status {
	case doctorOK:
		return "✓"
	case doctorWarn:
		return "!"
	case doctorFail:
		return "✗"
	default:
		return "ℹ"
	}
}

// logServeEnvironment writes one stderr line when the MCP server starts so the
// launcher's log (Claude Code, mcphub, Codex) shows the provider and whether an
// API key reached this process — before the first tool call fails.
func logServeEnvironment(w io.Writer, projectRoot string) {
	if projectRoot == "" {
		fmt.Fprintln(w, "vecgrep serve: no project detected in the working directory; tools report readiness once vecgrep_init runs")
		return
	}
	resolved, err := config.LoadResolved(projectRoot)
	if err != nil || resolved == nil || resolved.Config == nil {
		return
	}
	cfg := resolved.Config
	key := app.ResolveProviderKeyStatus(cfg)
	line := fmt.Sprintf("vecgrep serve: project=%s provider=%s model=%s", resolved.ProjectName, key.Provider, cfg.Embedding.Model)
	if key.RequiresKey {
		line += " api_key=" + key.Label()
	}
	if _, statErr := os.Stat(db.VecLitePath(cfg.DataDir)); statErr != nil {
		line += " index=not-built"
	}
	fmt.Fprintln(w, line)
	if key.Missing() {
		fmt.Fprintln(w, "vecgrep serve: "+embed.LauncherEnvHint)
	}
}
