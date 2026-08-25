package appnav

// The wire contract between the nav endpoint and assets/nav.js. It lives here,
// next to the script that parses it, so a field rename cannot land on one side
// only.
//
// The shape is deliberately a subset of what GET /api/apps already returns for
// the dashboard sidebar, using the same JSON names, so the switcher and the
// sidebar group and label an app identically without translating between two
// vocabularies.

// MaxApps caps how many apps one nav response carries. A visitor scanning a
// switcher is not paging through a fleet, and an uncapped list would put an
// unbounded query behind a page load on every app.
//
// Truncated on the payload says so out loud rather than letting a clipped list
// read as a complete one.
const MaxApps = 500

// App is one row in the switcher.
//
// Openable is a boolean rather than the raw lifecycle status on purpose. The
// switcher needs exactly one bit - can this be opened - and the status word
// carries more than that: an anonymous visitor sees public apps here, and
// "crashed" tells them something about an operator's morning that "unavailable"
// does not. The bit is not a secret either way, since requesting the app
// reveals it.
type App struct {
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	IconEmoji        string `json:"icon_emoji,omitempty"`
	ProjectSlug      string `json:"project_slug,omitempty"`
	ProjectName      string `json:"project_name,omitempty"`
	ProjectIconEmoji string `json:"project_icon_emoji,omitempty"`
	Openable         bool   `json:"openable"`
}

// Payload is the nav endpoint's response body.
type Payload struct {
	Apps []App `json:"apps"`
	// Username identifies whose list this is, so a visitor holding two
	// accounts can tell which one the switcher is showing before wondering where
	// an app went. Empty for an anonymous caller.
	Username string `json:"username,omitempty"`
	// Truncated reports that more apps exist than MaxApps returned.
	Truncated bool `json:"truncated,omitempty"`
}

// Openable reports whether an app in this lifecycle status can be opened by
// visiting it. A hibernated or waking app is openable: the request wakes it.
// A stopped, crashed or errored app is not - only an operator can bring it
// back, and offering it as a destination would send the visitor to a dead end.
func Openable(status string) bool {
	switch status {
	case "stopped", "crashed", "error":
		return false
	default:
		return true
	}
}
