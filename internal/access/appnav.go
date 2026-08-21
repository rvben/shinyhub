package access

import "github.com/rvben/shinyhub/internal/appnav"

// Option adjusts optional middleware behaviour. Options are variadic so a
// middleware that gains a feature does not force every existing call site to
// restate that it does not want it; the zero set is the behaviour these
// middlewares had before the switcher existed.
type Option func(*options)

type options struct {
	// nav carries the switcher's home URL and, by being non-nil, the fact that
	// it is enabled at all. One pointer rather than a bool beside a string, so
	// there is no state where the switcher is on with a home link nobody set.
	nav *navSettings
}

type navSettings struct{ homeURL string }

// WithAppNav injects the app switcher into the HTML pages these middlewares
// answer with themselves. Those pages are dead ends today: a visitor who is
// denied, or who lands on an app that has never been deployed, is left on a
// page with no way to reach the apps they can actually open. homeURL is the
// dashboard the switcher links to; empty means the current origin's root.
//
// The switcher fetches its contents from the nav endpoint, which answers with
// the apps THIS caller may see. On the 401 page that caller is anonymous, so
// the answer is the public apps and nothing else. The denied page itself still
// never names the app that was denied - see writeAccessDenied.
func WithAppNav(homeURL string) Option {
	return func(o *options) { o.nav = &navSettings{homeURL: homeURL} }
}

func newOptions(opts []Option) options {
	var o options
	for _, apply := range opts {
		if apply != nil {
			apply(&o)
		}
	}
	return o
}

// withAppNav returns page with the switcher spliced in before its closing body
// tag, or page unchanged when the switcher is off or the page has no place to
// put it. Declining leaves the page byte for byte as it was: the switcher is an
// addition to these pages, never a precondition for serving them.
func (o options) withAppNav(page []byte, slug string) []byte {
	if o.nav == nil || slug == "" {
		return page
	}
	out, ok := appnav.SpliceIntoBody(page, appnav.Snippet(slug, o.nav.homeURL))
	if !ok {
		return page
	}
	return out
}
