package prompt

import "sync"

// profileCache is only keyed by bundled registry profiles, never user input.
// Per-request instructions are appended after retrieving the immutable snapshot.
type profileCache struct{ entries sync.Map }

func (c *profileCache) get(profile string, build func() string) string {
	if v, ok := c.entries.Load(profile); ok {
		return v.(func() string)()
	}
	v, _ := c.entries.LoadOrStore(profile, sync.OnceValue(build))
	return v.(func() string)()
}
