package schedulespec

import (
	"time"

	"github.com/robfig/cron/v3"
)

// ProductionParser is the canonical cron parser for ShinyHub schedules and the
// single source of truth for the parser flags. Both the scheduler's missed-run
// dispatch and the freshness next-fire computation use it, so a stored cron
// expression can never be interpreted two different ways.
var ProductionParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// NextFire returns the first time the cron expression fires strictly after
// `after`, evaluated in `loc`. Stored expressions carry no TZ prefix (see
// Validate); the timezone is applied via the CRON_TZ form the parser accepts.
func NextFire(cronExpr string, loc *time.Location, after time.Time) (time.Time, error) {
	sched, err := ProductionParser.Parse("CRON_TZ=" + loc.String() + " " + cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after), nil
}
