package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
)

var (
	queueStateSessionKey string
	queueStateQueueKey   string

	queueBurnSessionKey   string
	queueBurnQueueKey     string
	queueBurnExecutor     string
	queueBurnAgentID      string
	queueBurnModel        string
	queueBurnHoursBudget  float64
	queueBurnTokensBudget int64
	queueBurnTokens       int64
	queueBurnDurationMS   int64
	queueBurnEndedAtMS    int64
	queueBurnUnbind       bool
)

var queueStateJSONCmd = &cobra.Command{
	Use:   "queue-state-json",
	Short: "Print SQLite-backed Sebastian queue state as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := queue.LoadState()
		if err != nil {
			return err
		}
		payload := map[string]interface{}{
			"bindings": state.Bindings,
			"items":    state.Items,
		}
		if queueStateSessionKey != "" {
			queueKey := state.Bindings[queueStateSessionKey]
			payload = map[string]interface{}{
				"session_key": queueStateSessionKey,
				"queue_key":   queueKey,
				"item":        state.Items[queueKey],
			}
		} else if queueStateQueueKey != "" {
			payload = map[string]interface{}{
				"queue_key": queueStateQueueKey,
				"item":      state.Items[queueStateQueueKey],
			}
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

var queueBurnOpenCmd = &cobra.Command{
	Use:   "queue-burn-open",
	Short: "Bind a session to a queue item, touch its lease, and return current burn state",
	RunE: func(cmd *cobra.Command, args []string) error {
		if queueBurnSessionKey == "" {
			return fmt.Errorf("--session-key is required")
		}
		return queue.WithQueueLock(func() error {
			state, err := queue.LoadState()
			if err != nil {
				return err
			}
			queueKey := queueBurnQueueKey
			if queueKey == "" {
				queueKey = state.Bindings[queueBurnSessionKey]
			}
			if queueKey == "" {
				return printJSON(map[string]interface{}{
					"found":       false,
					"session_key": queueBurnSessionKey,
				})
			}
			item, ok := state.Items[queueKey]
			if !ok {
				return printJSON(map[string]interface{}{
					"found":       false,
					"session_key": queueBurnSessionKey,
					"queue_key":   queueKey,
				})
			}
			queue.BindSession(state, queueBurnSessionKey, queueKey)
			if queueBurnHoursBudget > 0 {
				item.HoursBudget = queueBurnHoursBudget
			}
			if queueBurnTokensBudget > 0 {
				item.TokensBudget = queueBurnTokensBudget
			}
			if queueBurnExecutor != "" {
				item.Executor = queueBurnExecutor
			}
			queue.TouchLease(&item, queueBurnSessionKey, firstNonEmpty(queueBurnAgentID, queueBurnExecutor), queueBurnModel, time.Now())
			state.Items[queueKey] = item
			if err := state.Save(); err != nil {
				return err
			}
			burn := queue.ComputeBurn(item, time.Now())
			return printJSON(map[string]interface{}{
				"found":          true,
				"session_key":    queueBurnSessionKey,
				"queue_key":      queueKey,
				"item":           item,
				"burn":           burn,
				"advisory":       queueAdvisory(queueKey, item, burn),
				"hammer":         burn.Hammer,
				"hammer_changed": false,
			})
		})
	},
}

var queueBurnUsageCmd = &cobra.Command{
	Use:   "queue-burn-usage",
	Short: "Add token burn to a bound session lease and return hammer transition state",
	RunE: func(cmd *cobra.Command, args []string) error {
		if queueBurnSessionKey == "" {
			return fmt.Errorf("--session-key is required")
		}
		return queue.WithQueueLock(func() error {
			state, err := queue.LoadState()
			if err != nil {
				return err
			}
			queueKey := state.Bindings[queueBurnSessionKey]
			if queueKey == "" {
				return printJSON(map[string]interface{}{
					"found":       false,
					"session_key": queueBurnSessionKey,
				})
			}
			item := state.Items[queueKey]
			before := queue.ComputeBurn(item, time.Now()).Hammer
			after := queue.AddLeaseTokens(&item, queueBurnSessionKey, queueBurnTokens, queueBurnModel, time.Now())
			item.LastHammer = after
			state.Items[queueKey] = item
			if err := state.Save(); err != nil {
				return err
			}
			return printJSON(map[string]interface{}{
				"found":          true,
				"queue_key":      queueKey,
				"hammer_before":  before,
				"hammer_after":   after,
				"hammer_changed": before != after,
				"item":           item,
			})
		})
	},
}

var queueBurnCloseCmd = &cobra.Command{
	Use:   "queue-burn-close",
	Short: "Settle a bound session lease and optionally remove its binding",
	RunE: func(cmd *cobra.Command, args []string) error {
		if queueBurnSessionKey == "" {
			return fmt.Errorf("--session-key is required")
		}
		return queue.WithQueueLock(func() error {
			state, err := queue.LoadState()
			if err != nil {
				return err
			}
			queueKey := state.Bindings[queueBurnSessionKey]
			if queueKey == "" {
				return printJSON(map[string]interface{}{
					"found":       false,
					"session_key": queueBurnSessionKey,
				})
			}
			item := state.Items[queueKey]
			before := queue.ComputeBurn(item, time.Now()).Hammer
			endedAt := time.Now()
			if queueBurnEndedAtMS > 0 {
				endedAt = time.UnixMilli(queueBurnEndedAtMS)
			}
			var duration time.Duration
			if queueBurnDurationMS > 0 {
				duration = time.Duration(queueBurnDurationMS) * time.Millisecond
			}
			queue.SettleLease(&item, queueBurnSessionKey, endedAt, duration)
			if queueBurnUnbind {
				queue.UnbindSession(state, queueBurnSessionKey)
			}
			afterBurn := queue.ComputeBurn(item, time.Now())
			item.LastHammer = afterBurn.Hammer
			state.Items[queueKey] = item
			if err := state.Save(); err != nil {
				return err
			}
			return printJSON(map[string]interface{}{
				"found":          true,
				"queue_key":      queueKey,
				"hammer_before":  before,
				"hammer_after":   afterBurn.Hammer,
				"hammer_changed": before != afterBurn.Hammer,
				"item":           item,
			})
		})
	},
}

func queueAdvisory(queueKey string, item queue.StateItem, burn queue.BurnSnapshot) string {
	return fmt.Sprintf(
		"Sebastian queue: %s\nBurn: %s / %sh, %s / %s tk\nHammer: %s\nBurn is authoritative and system-computed. React to it; do not recalculate it.",
		queueKey,
		formatHoursBurn(burn.HoursSpent),
		formatHoursBurn(item.HoursBudget),
		formatTokensBurn(burn.TokensSpent),
		formatTokensBurn(item.TokensBudget),
		burn.Hammer,
	)
}

func formatHoursBurn(v float64) string {
	if v <= 0 {
		return "0"
	}
	r := queueRoundHours(v)
	if r == float64(int64(r)) {
		return fmt.Sprintf("%.0f", r)
	}
	return fmt.Sprintf("%.1f", r)
}

func queueRoundHours(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func formatTokensBurn(v int64) string {
	if v < 1000 {
		return fmt.Sprintf("%d", v)
	}
	if v < 1_000_000 {
		return trimBurn(float64(v)/1000) + "k"
	}
	return trimBurn(float64(v)/1_000_000) + "M"
}

func trimBurn(v float64) string {
	out := fmt.Sprintf("%.1f", v)
	if len(out) >= 2 && out[len(out)-2:] == ".0" {
		return out[:len(out)-2]
	}
	return out
}

func printJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func init() {
	queueStateJSONCmd.Flags().StringVar(&queueStateSessionKey, "session-key", "", "Lookup queue state by session binding")
	queueStateJSONCmd.Flags().StringVar(&queueStateQueueKey, "queue-key", "", "Lookup queue state by queue key")

	for _, c := range []*cobra.Command{queueBurnOpenCmd, queueBurnUsageCmd, queueBurnCloseCmd} {
		c.Flags().StringVar(&queueBurnSessionKey, "session-key", "", "Session key")
		c.Flags().StringVar(&queueBurnQueueKey, "queue-key", "", "Queue key")
		c.Flags().StringVar(&queueBurnExecutor, "executor", "", "Executor agent id")
		c.Flags().StringVar(&queueBurnAgentID, "agent-id", "", "Agent id")
		c.Flags().StringVar(&queueBurnModel, "model", "", "Model name")
	}
	queueBurnOpenCmd.Flags().Float64Var(&queueBurnHoursBudget, "hours-budget", 0, "Hours appetite budget override")
	queueBurnOpenCmd.Flags().Int64Var(&queueBurnTokensBudget, "tokens-budget", 0, "Token appetite budget override")
	queueBurnUsageCmd.Flags().Int64Var(&queueBurnTokens, "tokens", 0, "Token delta to add")
	queueBurnCloseCmd.Flags().Int64Var(&queueBurnDurationMS, "duration-ms", 0, "Explicit duration override in milliseconds")
	queueBurnCloseCmd.Flags().Int64Var(&queueBurnEndedAtMS, "ended-at-ms", 0, "End timestamp in unix milliseconds")
	queueBurnCloseCmd.Flags().BoolVar(&queueBurnUnbind, "unbind", false, "Remove the session binding after settling")
}
