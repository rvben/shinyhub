package deploy

import (
	"fmt"
	"sort"
)

// CheckColocatedShared verifies that every node a consumer runs on also hosts
// each shared-mount source. consumerTiers are the tiers the consumer is placed
// on. sources maps each mounted source slug to the tiers that source runs on.
// nodeForTier resolves a tier to the node id that backs it. Returns an error
// describing the first cross-node mount it finds.
func CheckColocatedShared(consumerTiers []string, sources map[string][]string, nodeForTier func(string) string) error {
	for _, consumerTier := range consumerTiers {
		consumerNode := nodeForTier(consumerTier)
		for sourceSlug, sourceTiers := range sources {
			sourceNodes := make(map[string]bool, len(sourceTiers))
			for _, st := range sourceTiers {
				sourceNodes[nodeForTier(st)] = true
			}
			if !sourceNodes[consumerNode] {
				have := make([]string, 0, len(sourceNodes))
				for n := range sourceNodes {
					have = append(have, n)
				}
				sort.Strings(have)
				return fmt.Errorf(
					"shared mount %q is not available on node %q (source runs on %v); cross-node shared mounts are not supported",
					sourceSlug, consumerNode, have)
			}
		}
	}
	return nil
}
