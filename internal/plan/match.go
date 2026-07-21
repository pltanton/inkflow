package plan

import (
	"fmt"
	"log/slog"
	"strings"

	"inkflow/internal/config"
)

type Match struct {
	Route     config.Route
	Matched   bool
	Remainder string
}

func Select(routes []config.Route, sourcePath string) (Match, error) {
	sp := config.NormalizeRoutePrefix(sourcePath)
	bestLen := -1
	var best Match
	ambiguous := false
	for _, r := range routes {
		from := config.NormalizeRoutePrefix(r.From)
		if from == "" || !strings.HasPrefix(sp, from) {
			continue
		}
		if len(from) <= bestLen {
			if len(from) == bestLen {
				ambiguous = true
			}
			continue
		}
		bestLen = len(from)
		best = Match{Route: r, Matched: true, Remainder: strings.Trim(strings.TrimPrefix(sp, from), "/")}
		ambiguous = false
	}
	if ambiguous {
		slog.Default().Debug("route_match_ambiguous", "source", sourcePath)
		return Match{}, fmt.Errorf("ambiguous route match for %q", sourcePath)
	}
	if !best.Matched {
		slog.Default().Debug("no_route_matched", "source", sourcePath)
		return best, nil
	}
	slog.Default().Debug("route_match_selected", "source", sourcePath, "from", best.Route.From, "remainder", best.Remainder)
	return best, nil
}
