package report

import (
	"fmt"

	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// TrendPoint is a player's statistics in one calendar month.
type TrendPoint struct {
	Month         string  `json:"month"` // YYYY-MM
	Missions      int     `json:"missions"`
	Kills         int     `json:"kills"`
	Deaths        int     `json:"deaths"`
	KillAssists   int     `json:"killAssists"`
	MedicalPoints float64 `json:"medicalPoints"`
	KD            float64 `json:"kd"` // kills / deaths (kills when there are no deaths); 0 without missions
}

// Trend sums a player's rows (ordered by date) per month, from the first month played to the
// last; months without missions are kept, with zeros.
func Trend(rows []store.Row, med stats.Medical) []TrendPoint {
	if len(rows) == 0 {
		return nil
	}
	month := func(date string) string {
		if len(date) >= 7 {
			return date[:7]
		}
		return date
	}
	var out []TrendPoint
	missions := map[int64]bool{}
	var treatments, revives int
	finish := func() {
		p := &out[len(out)-1]
		p.Missions = len(missions)
		p.KD = round2(float64(p.Kills) / float64(max(p.Deaths, 1)))
		p.MedicalPoints = med.Points(treatments, revives)
	}
	for _, row := range rows {
		m := month(row.Date)
		if len(out) == 0 || out[len(out)-1].Month != m {
			if len(out) > 0 {
				finish()
				for gap := nextMonth(out[len(out)-1].Month); gap < m; gap = nextMonth(gap) {
					out = append(out, TrendPoint{Month: gap})
				}
			}
			out = append(out, TrendPoint{Month: m})
			missions, treatments, revives = map[int64]bool{}, 0, 0
		}
		p := &out[len(out)-1]
		p.Kills += row.Kills
		p.Deaths += row.Deaths
		p.KillAssists += row.KillAssists
		treatments += row.Treatments
		revives += row.Revives
		missions[row.MissionID] = true
	}
	finish()
	return out
}

// nextMonth returns the month after a YYYY-MM month.
func nextMonth(m string) string {
	var y, mo int
	if _, err := fmt.Sscanf(m, "%d-%d", &y, &mo); err != nil {
		return "9999-12" // ends a gap loop on a malformed date
	}
	if mo == 12 {
		return fmt.Sprintf("%04d-01", y+1)
	}
	return fmt.Sprintf("%04d-%02d", y, mo+1)
}
