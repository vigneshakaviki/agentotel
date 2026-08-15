// Command agentotel is a local reverse proxy that transparently traces any
// AI agent's LLM API calls — cost, tokens, latency — with zero SDK
// integration required on the agent side.
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
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
	var last, groupBy string
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Show recently recorded spans",
		RunE: func(cmd *cobra.Command, args []string) error {
			since, err := time.ParseDuration(last)
			if err != nil {
				return fmt.Errorf("invalid --last duration %q: %w", last, err)
			}
			key, err := groupKeyFunc(groupBy)
			if err != nil {
				return err
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

			if key != nil {
				printGrouped(spans, strings.ToUpper(groupBy), key)
			} else {
				printSpans(spans)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&last, "last", "1h", "how far back to look, e.g. 30m, 1h, 24h")
	cmd.Flags().StringVar(&groupBy, "by", "", "summarize spend by one of: agent, model, provider, session")
	return cmd
}

// groupKeyFunc maps a --by value to the span field it groups on. Returns a
// nil func (and no error) for the default per-call listing.
func groupKeyFunc(by string) (func(store.Span) string, error) {
	switch by {
	case "":
		return nil, nil
	case "agent":
		return func(sp store.Span) string { return orUnknown(sp.Agent) }, nil
	case "model":
		return func(sp store.Span) string { return orUnknown(sp.Model) }, nil
	case "provider":
		return func(sp store.Span) string { return orUnknown(sp.Provider) }, nil
	case "session":
		return func(sp store.Span) string { return orUnknown(sp.SessionID) }, nil
	default:
		return nil, fmt.Errorf("invalid --by %q: want agent, model, provider, or session", by)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func printSpans(spans []store.Span) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tAGENT\tPROVIDER\tMODEL\tIN\tCACHED\tOUT\tCOST\tLATENCY")
	var totalCost float64
	for _, sp := range spans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t$%.4f\t%dms\n",
			sp.TS.Local().Format("15:04:05"), orUnknown(sp.Agent), sp.Provider, sp.Model,
			sp.InputTokens, sp.CacheReadTokens+sp.CacheWriteTokens, sp.OutputTokens,
			sp.CostUSD, sp.LatencyMS)
		totalCost += sp.CostUSD
	}
	w.Flush()
	fmt.Printf("\n%d calls, $%.4f total\n", len(spans), totalCost)
}

type group struct {
	name            string
	calls           int
	in, cached, out int
	cost            float64
	totalLatencyMS  int64
}

func printGrouped(spans []store.Span, label string, key func(store.Span) string) {
	byKey := map[string]*group{}
	var order []string
	for _, sp := range spans {
		k := key(sp)
		g, ok := byKey[k]
		if !ok {
			g = &group{name: k}
			byKey[k] = g
			order = append(order, k)
		}
		g.calls++
		g.in += sp.InputTokens
		g.cached += sp.CacheReadTokens + sp.CacheWriteTokens
		g.out += sp.OutputTokens
		g.cost += sp.CostUSD
		g.totalLatencyMS += sp.LatencyMS
	}

	groups := make([]*group, 0, len(order))
	for _, k := range order {
		groups = append(groups, byKey[k])
	}
	// Most expensive first — the question this view answers is "what is
	// costing me money", not "what ran most recently".
	sort.Slice(groups, func(i, j int) bool { return groups[i].cost > groups[j].cost })

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "%s\tCALLS\tIN\tCACHED\tOUT\tCOST\tAVG LATENCY\n", label)
	var totalCost float64
	var totalCalls int
	for _, g := range groups {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t$%.4f\t%dms\n",
			g.name, g.calls, g.in, g.cached, g.out, g.cost, g.totalLatencyMS/int64(g.calls))
		totalCost += g.cost
		totalCalls += g.calls
	}
	w.Flush()
	fmt.Printf("\n%d calls across %d %s(s), $%.4f total\n",
		totalCalls, len(groups), strings.ToLower(label), totalCost)
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
