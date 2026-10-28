package report

import (
	"slices"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

func TestTrend(t *testing.T) {
	row := func(id int64, date string, kills, deaths int) store.Row {
		r := store.Row{MissionID: id, Date: date}
		r.Kills, r.Deaths = kills, deaths
		return r
	}
	got := Trend([]store.Row{
		row(1, "2025-11-03", 3, 1),
		row(2, "2025-11-20", 1, 0),
		row(3, "2026-02-01", 0, 2), // December and January without missions
	}, stats.Medical{})
	want := []TrendPoint{
		{Month: "2025-11", Missions: 2, Kills: 4, Deaths: 1, KD: 4},
		{Month: "2025-12"},
		{Month: "2026-01"},
		{Month: "2026-02", Missions: 1, Deaths: 2},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Trend = %+v\nwant %+v", got, want)
	}
	if Trend(nil, stats.Medical{}) != nil {
		t.Error("trend without rows")
	}
}
