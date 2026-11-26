package webui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const statisticsFragment = "/ui/fragments/statistics"

type statisticsStage struct {
	Name  string
	Count int64
}

type statisticsView struct {
	Snapshot domain.OperatorStatistics
	Stages   []statisticsStage
}

func (h *Handler) statistics(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	result, err := h.dependencies.Operator.Statistics(r.Context())
	if err != nil {
		h.readError(w, err)
		return
	}
	stages := []statisticsStage{
		{"Received", result.Product.OperationsByStatus[domain.StatusReceived]},
		{"Planned", result.Product.OperationsByStatus[domain.StatusPlanned]},
		{"Awaiting review", result.Product.OperationsByStatus[domain.StatusAwaitingReview]},
		{"Applying", result.Product.OperationsByStatus[domain.StatusApplying]},
		{"Committed", result.Product.OperationsByStatus[domain.StatusCommitted]},
		{"Failed", result.Product.OperationsByStatus[domain.StatusFailed]},
	}
	h.render(w, http.StatusOK, "statistics", statisticsView{Snapshot: result, Stages: stages})
}

func statisticsBytes(metric domain.StatisticsMetric) string {
	if metric.Availability != domain.StatisticsAvailable {
		reason := strings.ReplaceAll(string(metric.Availability), "_", " ")
		if reason == "" {
			return unavailableLabel
		}
		return fmt.Sprintf("%s (%s)", unavailableLabel, reason)
	}
	value := float64(metric.Value)
	units := [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", metric.Value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
