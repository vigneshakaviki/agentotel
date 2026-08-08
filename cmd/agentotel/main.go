// Command agentotel is a local reverse proxy that transparently traces any
// AI agent's LLM API calls — cost, tokens, latency — with zero SDK
// integration required on the agent side.
package main

import (
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"agentotel/internal/pricing"
	"agentotel/internal/proxy"
	"agentotel/internal/store"
)

const version = "0.1.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "agentotel",
		Short: "Trace any AI agent's LLM API calls — zero SDK, zero code changes.",
		Long: `agentotel is a local reverse proxy for LLM provider APIs.

Point your agent's OpenAI/Anthropic base URL at agentotel instead of the
real API, and every call is transparently traced (tokens, cost, latency)
and forwarded on unmodified — no SDK, no code changes.`,
	}
	root.AddCommand(startCmd(), traceCmd(), versionCmd())
	return root
}

func startCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Run the tracing proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, err := store.DefaultPath()
			if err != nil {
				return err
			}
			st, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			defer st.Close()

			prices, err := pricing.Default()
			if err != nil {
				return err
			}

			handler, err := proxy.New(st, prices)
			if err != nil {
				return err
			}

			addr := fmt.Sprintf(":%d", port)
			fmt.Printf("agentotel listening on %s\n", addr)
			fmt.Println("  OpenAI:    export OPENAI_BASE_URL=http://localhost" + addr + "/openai")
			fmt.Println("  Anthropic: export ANTHROPIC_BASE_URL=http://localhost" + addr + "/anthropic")
			fmt.Printf("Recording spans to %s\n", dbPath)

			return http.ListenAndServe(addr, handler)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8787, "port to listen on")
	return cmd
}

func traceCmd() *cobra.Command {
	var last string
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Show recently recorded spans",
		RunE: func(cmd *cobra.Command, args []string) error {
			since, err := time.ParseDuration(last)
			if err != nil {
				return fmt.Errorf("invalid --last duration %q: %w", last, err)
			}

			dbPath, err := store.DefaultPath()
			if err != nil {
				return err
			}
			st, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			defer st.Close()

			spans, err := st.RecentSpans(since)
			if err != nil {
				return err
			}
			if len(spans) == 0 {
				fmt.Println("No spans recorded in that window yet.")
				fmt.Println("Run `agentotel start` and point an agent's base URL at it first.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "TIME\tPROVIDER\tMODEL\tIN\tOUT\tCOST\tLATENCY")
			var totalCost float64
			for _, sp := range spans {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t$%.4f\t%dms\n",
					sp.TS.Local().Format("15:04:05"), sp.Provider, sp.Model,
					sp.InputTokens, sp.OutputTokens, sp.CostUSD, sp.LatencyMS)
				totalCost += sp.CostUSD
			}
			w.Flush()
			fmt.Printf("\n%d calls, $%.4f total\n", len(spans), totalCost)
			return nil
		},
	}
	cmd.Flags().StringVar(&last, "last", "1h", "how far back to look, e.g. 30m, 1h, 24h")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the agentotel version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("agentotel " + version)
		},
	}
}
