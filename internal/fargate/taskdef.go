package fargate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// maxFamilyPrefix bounds the sanitized-prefix portion of a task-def family so
// the whole name (prefix + hash + "-app-" + a 64-bit id) stays under ECS's
// 255-character family limit with comfortable headroom.
const maxFamilyPrefix = 200

// taskDefFamily derives the per-app task-definition family from the install
// prefix and app id. ECS families allow only letters, digits, hyphens, and
// underscores, so any other character in the prefix (notably the slashes used
// in secret names) is replaced with a hyphen; the sanitized prefix is kept for
// readability but is also disambiguated with a short hash of the RAW prefix, so
// two prefixes that sanitize to the same string (e.g. "a/b" and "a-b") still
// map to distinct families. The app id scopes the family so a delete-then-
// recreate of the same slug lands on its own family.
func taskDefFamily(prefix string, appID int64) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, prefix)
	if len(sanitized) > maxFamilyPrefix {
		sanitized = sanitized[:maxFamilyPrefix]
	}
	sum := sha256.Sum256([]byte(prefix))
	h := hex.EncodeToString(sum[:])[:8]
	core := h
	if sanitized != "" {
		core = sanitized + "-" + h
	}
	return fmt.Sprintf("%s-app-%d", core, appID)
}

// mergeSecrets returns base secrets with app secrets layered on top: an app
// secret replaces a base secret of the same name (app wins), and base secrets
// the app does not name are preserved. The inputs are not mutated.
func mergeSecrets(base, app []ecstypes.Secret) []ecstypes.Secret {
	if len(base) == 0 {
		return app
	}
	appByName := make(map[string]struct{}, len(app))
	for _, s := range app {
		appByName[aws.ToString(s.Name)] = struct{}{}
	}
	out := make([]ecstypes.Secret, 0, len(base)+len(app))
	for _, s := range base {
		if _, overridden := appByName[aws.ToString(s.Name)]; overridden {
			continue
		}
		out = append(out, s)
	}
	out = append(out, app...)
	return out
}

// buildTaskDefInput clones the operator's base task definition into a
// RegisterTaskDefinition request under the per-app family, merging the given
// secrets block onto the named container. The base supplies the container
// image, roles, network mode, and resource shape; the app's secrets are layered
// on top of any the operator already configured on that container (app wins on
// a name collision, other base secrets are preserved). The base value is not
// mutated.
//
// secrets entries reference Secrets Manager ARNs via valueFrom; the ECS agent
// resolves them at task start using the execution role, so the values never
// appear in ecs:DescribeTasks/DescribeTaskDefinition.
func buildTaskDefInput(base *ecstypes.TaskDefinition, family, containerName string, secrets []ecstypes.Secret) (*ecs.RegisterTaskDefinitionInput, error) {
	if base == nil {
		return nil, fmt.Errorf("fargate: nil base task definition")
	}
	containers := make([]ecstypes.ContainerDefinition, len(base.ContainerDefinitions))
	copy(containers, base.ContainerDefinitions)

	found := false
	for i := range containers {
		if aws.ToString(containers[i].Name) == containerName {
			containers[i].Secrets = mergeSecrets(containers[i].Secrets, secrets)
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("fargate: base task definition has no container named %q", containerName)
	}

	return &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		ContainerDefinitions:    containers,
		ExecutionRoleArn:        base.ExecutionRoleArn,
		TaskRoleArn:             base.TaskRoleArn,
		NetworkMode:             base.NetworkMode,
		RequiresCompatibilities: base.RequiresCompatibilities,
		Cpu:                     base.Cpu,
		Memory:                  base.Memory,
		Volumes:                 base.Volumes,
		PlacementConstraints:    base.PlacementConstraints,
		RuntimePlatform:         base.RuntimePlatform,
		EphemeralStorage:        base.EphemeralStorage,
		IpcMode:                 base.IpcMode,
		PidMode:                 base.PidMode,
		ProxyConfiguration:      base.ProxyConfiguration,
	}, nil
}
