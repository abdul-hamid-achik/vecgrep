package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/spf13/cobra"
)

// printIndexPlan writes a dry-run plan to stdout (shared by --dry-run and pre-index confirm).
func printIndexPlan(root string, p *index.DryRunPreview) {
	if p == nil {
		return
	}
	fmt.Printf("Plan for %s\n", root)
	if p.ScannedFiles > 0 {
		fmt.Printf("  Scanned files:    %d\n", p.ScannedFiles)
	}
	if p.BytesScanned > 0 {
		fmt.Printf("  Scanned size:     %s\n", formatPlanBytes(p.BytesScanned))
	}
	fmt.Printf("  New files:        %d\n", p.NewFiles)
	fmt.Printf("  Modified files:   %d\n", p.ModifiedFiles)
	fmt.Printf("  Deleted files:    %d\n", p.DeletedFiles)
	fmt.Printf("  Files to embed:   %d\n", p.FilesToEmbed)
	fmt.Printf("  Estimated chunks: %d\n", p.EstimatedChunks)
}

// needsInteractiveIndexConfirm reports whether a TTY should require y/n before embedding.
// Full reindex and empty indexes always confirm; large plans use DryRunPreview.NeedsConfirm.
func needsInteractiveIndexConfirm(full, empty bool, p *index.DryRunPreview) bool {
	if full || empty {
		return true
	}
	return p != nil && p.NeedsConfirm()
}

// confirmIndexPlanTTY prints the plan and asks y/n. Returns true if the user confirmed.
// Caller should only invoke when stdin/stdout are interactive.
func confirmIndexPlanTTY(root string, full, empty bool, p *index.DryRunPreview) (bool, error) {
	printIndexPlan(root, p)
	fmt.Println()
	kind := "index"
	switch {
	case full:
		kind = "full reindex"
	case empty:
		kind = "first index"
	case p != nil && p.NeedsConfirm():
		kind = "large scope index"
	}
	fmt.Printf("Proceed with %s of %s? [y/N] ", kind, root)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(strings.TrimSpace(line)) == 0 {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return true, nil
	}
	return false, nil
}

func formatPlanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// maybeConfirmIndexPlan runs a dry-run when full or empty (or always when we
// need a large-scope check), then prompts on TTY unless --yes.
// Returns a non-nil error only on dry-run failure or user cancel.
func maybeConfirmIndexPlan(cmd *cobra.Command, service *app.Service, root, structuralMode string, full, yes bool) error {
	indexed, _, chunks, metaErr := service.IndexMeta(cmd.Context())
	empty := metaErr == nil && (!indexed || chunks == 0)

	// Cheap path: non-empty incremental without --full never plans (Studio matches).
	// Large wrong folders that are already partially indexed still get live
	// progress + soft warn during Index itself.
	if !full && !empty {
		return nil
	}

	preview, err := service.DryRunPreviewWithStructuralMode(cmd.Context(), structuralMode)
	if err != nil {
		return fmt.Errorf("plan failed: %w", err)
	}

	// Nothing to do and not a full reindex: say so and exit cleanly without embed.
	if !full && preview.TotalPending == 0 && preview.FilesToEmbed == 0 {
		printIndexPlan(root, preview)
		fmt.Println("\nIndex is up to date — nothing to do.")
		return errIndexNothingToDo
	}

	// Small empty projects (Ollama-local): show plan briefly, auto-start.
	// Mirrors Studio emptyIndexAutoStartMax = 100.
	const emptyAutoStartMax = 100
	if empty && !full && preview.ScannedFiles > 0 && preview.ScannedFiles < emptyAutoStartMax && !preview.NeedsConfirm() {
		printIndexPlan(root, preview)
		fmt.Println("\nSmall first index — starting without confirm (use a larger tree or --full for y/n).")
		fmt.Println()
		return nil
	}

	need := needsInteractiveIndexConfirm(full, empty, preview)
	if !need {
		return nil
	}

	if yes || !isInteractiveTerminal() {
		// Non-TTY / --yes: print plan for visibility, then proceed.
		printIndexPlan(root, preview)
		if !yes && !isInteractiveTerminal() {
			fmt.Fprintln(os.Stderr, "note: non-interactive session — proceeding without confirm (pass --yes to silence)")
		}
		fmt.Println()
		return nil
	}

	ok, err := confirmIndexPlanTTY(root, full, empty, preview)
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !ok {
		fmt.Println("Index cancelled.")
		return errIndexCancelled
	}
	fmt.Println()
	return nil
}

// Sentinel errors so runIndex can exit 0 on cancel / nothing-to-do without
// printing cobra's "Error:" prefix when we already wrote a friendly message.
type quietExitError struct {
	msg string
}

func (e quietExitError) Error() string { return e.msg }

var (
	errIndexCancelled   = quietExitError{msg: "index cancelled"}
	errIndexNothingToDo = quietExitError{msg: "index up to date"}
)
