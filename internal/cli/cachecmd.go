package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
)

func newCacheCmd(st *state) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clean the clone cache",
	}

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List cached repository clones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cacheDir := st.loaded.Config.Checkout.CacheDir
			repos, err := checkout.ListCache(cacheDir)
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				cmd.Println("cache is empty:", cacheDir)
				return nil
			}
			var total int64
			for _, r := range repos {
				total += r.Size
				last := "never fetched"
				if !r.LastUse.IsZero() {
					last = r.LastUse.Format("2006-01-02 15:04")
				}
				cmd.Printf("%8s  %-50s last used %s\n", fmtSize(r.Size), r.Project, last)
			}
			cmd.Printf("\n%s total in %s\n", fmtSize(total), cacheDir)
			return nil
		},
	}

	var all bool
	clean := &cobra.Command{
		Use:   "clean",
		Short: "Remove review worktrees and evict clones over the cache budget",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config.Checkout
			res, err := checkout.CleanCache(cmd.Context(), cfg.CacheDir, cfg.CacheMaxMB, all)
			if err != nil {
				return err
			}
			if len(res.Removed) == 0 {
				cmd.Println("nothing to clean")
				return nil
			}
			for _, r := range res.Removed {
				cmd.Println("removed", r)
			}
			cmd.Printf("freed %s\n", fmtSize(res.FreedBytes))
			return nil
		},
	}
	clean.Flags().BoolVar(&all, "all", false, "remove every cached clone, not just over-budget ones")

	cmd.AddCommand(ls, clean)
	return cmd
}

func fmtSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
