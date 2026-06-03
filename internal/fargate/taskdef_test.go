package fargate

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func baseTaskDef() *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{
		Family:                  aws.String("shinyhub-runner"),
		ExecutionRoleArn:        aws.String("arn:aws:iam::111122223333:role/exec"),
		TaskRoleArn:             aws.String("arn:aws:iam::111122223333:role/task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:  aws.String("app"),
				Image: aws.String("ghcr.io/example/runner:latest"),
			},
			{
				Name:  aws.String("sidecar"),
				Image: aws.String("ghcr.io/example/sidecar:latest"),
			},
		},
	}
}

var familyCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestTaskDefFamily_ValidCharsetAndIncludesAppID(t *testing.T) {
	// ECS task-definition families allow only letters, digits, hyphens, and
	// underscores, so the slash-bearing secret prefix must be sanitized. The
	// family must remain recognizable (carry the sanitized prefix) and scope by
	// app id.
	got := taskDefFamily("shinyhub/prod", 7)
	if !familyCharset.MatchString(got) {
		t.Errorf("family %q contains characters invalid for an ECS family", got)
	}
	if !strings.Contains(got, "shinyhub-prod") {
		t.Errorf("family %q should carry the sanitized prefix for readability", got)
	}
	if !strings.HasSuffix(got, "-app-7") {
		t.Errorf("family %q should be scoped by app id (suffix -app-7)", got)
	}
}

func TestTaskDefFamily_DifferentAppsDiffer(t *testing.T) {
	if taskDefFamily("p", 1) == taskDefFamily("p", 2) {
		t.Error("families for different apps must differ")
	}
}

// TestTaskDefFamily_DistinctPrefixesDoNotCollide guards that two prefixes which
// sanitize to the same string still map to different families (different
// installs sharing an AWS account must not clobber each other's task defs).
func TestTaskDefFamily_DistinctPrefixesDoNotCollide(t *testing.T) {
	a := taskDefFamily("shinyhub/prod", 7)
	b := taskDefFamily("shinyhub-prod", 7)
	if a == b {
		t.Errorf("distinct prefixes %q and %q produced the same family %q", "shinyhub/prod", "shinyhub-prod", a)
	}
}

func TestTaskDefFamily_BoundedLength(t *testing.T) {
	// ECS caps a family name at 255 characters; a long operator prefix must not
	// blow past it.
	long := strings.Repeat("x", 400)
	got := taskDefFamily(long, 123456)
	if len(got) > 255 {
		t.Errorf("family length = %d, must be <= 255", len(got))
	}
	if !familyCharset.MatchString(got) {
		t.Errorf("family %q invalid charset", got)
	}
}

func TestTaskDefFamily_EmptyPrefix(t *testing.T) {
	got := taskDefFamily("", 5)
	if !familyCharset.MatchString(got) || !strings.HasSuffix(got, "-app-5") {
		t.Errorf("taskDefFamily empty prefix = %q, want a valid family scoped by app 5", got)
	}
}

func TestFamilyOfTaskDefARN(t *testing.T) {
	cases := map[string]string{
		"arn:aws:ecs:eu-west-1:111122223333:task-definition/shinyhub-prod-app-7:3": "shinyhub-prod-app-7",
		"shinyhub-prod-app-7:1": "shinyhub-prod-app-7",
		"no-revision":           "",
	}
	for arn, want := range cases {
		if got := familyOfTaskDefARN(arn); got != want {
			t.Errorf("familyOfTaskDefARN(%q) = %q, want %q", arn, got, want)
		}
	}
}

func TestAppSecretPrefix_TrailingSlashBoundary(t *testing.T) {
	// The trailing slash makes app 1 and app 11 disjoint prefixes.
	if p := appSecretPrefix("shinyhub/prod", 1); p != "shinyhub/prod/app-1/" {
		t.Errorf("appSecretPrefix = %q", p)
	}
	if strings.HasPrefix("shinyhub/prod/app-11/X", appSecretPrefix("shinyhub/prod", 1)) {
		t.Error("app-1 prefix must not match app-11 secrets")
	}
}

func TestBuildTaskDefInput_InjectsSecretsOnNamedContainer(t *testing.T) {
	secrets := []ecstypes.Secret{
		{Name: aws.String("AWS_SECRET"), ValueFrom: aws.String("arn:aws:secretsmanager:eu-west-1:111122223333:secret:p/app-7/AWS_SECRET-AbC")},
	}
	in, err := buildTaskDefInput(baseTaskDef(), "shinyhub-app-7", "app", secrets)
	if err != nil {
		t.Fatalf("buildTaskDefInput: %v", err)
	}
	if aws.ToString(in.Family) != "shinyhub-app-7" {
		t.Errorf("Family = %q, want shinyhub-app-7", aws.ToString(in.Family))
	}
	// The named container carries the secrets block; the other does not.
	var appC, sideC *ecstypes.ContainerDefinition
	for i := range in.ContainerDefinitions {
		switch aws.ToString(in.ContainerDefinitions[i].Name) {
		case "app":
			appC = &in.ContainerDefinitions[i]
		case "sidecar":
			sideC = &in.ContainerDefinitions[i]
		}
	}
	if appC == nil || sideC == nil {
		t.Fatalf("expected both containers cloned, got %d", len(in.ContainerDefinitions))
	}
	if len(appC.Secrets) != 1 || aws.ToString(appC.Secrets[0].Name) != "AWS_SECRET" {
		t.Errorf("app container secrets = %+v", appC.Secrets)
	}
	if aws.ToString(appC.Secrets[0].ValueFrom) == "" {
		t.Error("secret ValueFrom (ARN) must be set")
	}
	if len(sideC.Secrets) != 0 {
		t.Errorf("sidecar must not receive the app secrets, got %+v", sideC.Secrets)
	}
	// Image and other container fields are preserved.
	if aws.ToString(appC.Image) != "ghcr.io/example/runner:latest" {
		t.Errorf("app image not preserved: %q", aws.ToString(appC.Image))
	}
}

func TestBuildTaskDefInput_CopiesTaskLevelFields(t *testing.T) {
	in, err := buildTaskDefInput(baseTaskDef(), "fam", "app", nil)
	if err != nil {
		t.Fatalf("buildTaskDefInput: %v", err)
	}
	if aws.ToString(in.ExecutionRoleArn) != "arn:aws:iam::111122223333:role/exec" {
		t.Errorf("ExecutionRoleArn not copied: %q", aws.ToString(in.ExecutionRoleArn))
	}
	if aws.ToString(in.TaskRoleArn) != "arn:aws:iam::111122223333:role/task" {
		t.Errorf("TaskRoleArn not copied: %q", aws.ToString(in.TaskRoleArn))
	}
	if in.NetworkMode != ecstypes.NetworkModeAwsvpc {
		t.Errorf("NetworkMode not copied: %q", in.NetworkMode)
	}
	if len(in.RequiresCompatibilities) != 1 || in.RequiresCompatibilities[0] != ecstypes.CompatibilityFargate {
		t.Errorf("RequiresCompatibilities not copied: %v", in.RequiresCompatibilities)
	}
	if aws.ToString(in.Cpu) != "256" || aws.ToString(in.Memory) != "512" {
		t.Errorf("Cpu/Memory not copied: %q/%q", aws.ToString(in.Cpu), aws.ToString(in.Memory))
	}
}

// TestBuildTaskDefInput_PreservesBaseSecrets guards that operator-configured
// secrets on the base runner container survive cloning: a base secret with a
// distinct name must remain, alongside the app's secrets.
func TestBuildTaskDefInput_PreservesBaseSecrets(t *testing.T) {
	base := baseTaskDef()
	base.ContainerDefinitions[0].Secrets = []ecstypes.Secret{
		{Name: aws.String("RUNNER_TOKEN"), ValueFrom: aws.String("arn:operator")},
	}
	in, err := buildTaskDefInput(base, "fam", "app", []ecstypes.Secret{
		{Name: aws.String("APP_SECRET"), ValueFrom: aws.String("arn:app")},
	})
	if err != nil {
		t.Fatalf("buildTaskDefInput: %v", err)
	}
	got := map[string]string{}
	for _, s := range in.ContainerDefinitions[0].Secrets {
		got[aws.ToString(s.Name)] = aws.ToString(s.ValueFrom)
	}
	if got["RUNNER_TOKEN"] != "arn:operator" {
		t.Errorf("base container secret RUNNER_TOKEN was dropped: %v", got)
	}
	if got["APP_SECRET"] != "arn:app" {
		t.Errorf("app secret APP_SECRET missing: %v", got)
	}
}

// TestBuildTaskDefInput_AppSecretOverridesBaseSameName guards that on a name
// collision the app's secret wins (exactly one entry, the app's ARN).
func TestBuildTaskDefInput_AppSecretOverridesBaseSameName(t *testing.T) {
	base := baseTaskDef()
	base.ContainerDefinitions[0].Secrets = []ecstypes.Secret{
		{Name: aws.String("K"), ValueFrom: aws.String("arn:base")},
	}
	in, err := buildTaskDefInput(base, "fam", "app", []ecstypes.Secret{
		{Name: aws.String("K"), ValueFrom: aws.String("arn:app")},
	})
	if err != nil {
		t.Fatalf("buildTaskDefInput: %v", err)
	}
	secrets := in.ContainerDefinitions[0].Secrets
	if len(secrets) != 1 {
		t.Fatalf("expected exactly one secret named K, got %+v", secrets)
	}
	if aws.ToString(secrets[0].ValueFrom) != "arn:app" {
		t.Errorf("app secret should win on name collision, got %q", aws.ToString(secrets[0].ValueFrom))
	}
}

func TestBuildTaskDefInput_ErrorsWhenContainerMissing(t *testing.T) {
	_, err := buildTaskDefInput(baseTaskDef(), "fam", "does-not-exist", nil)
	if err == nil {
		t.Fatal("expected an error when the named container is absent from the base task definition")
	}
}

func TestBuildTaskDefInput_DoesNotMutateBase(t *testing.T) {
	base := baseTaskDef()
	_, err := buildTaskDefInput(base, "fam", "app", []ecstypes.Secret{
		{Name: aws.String("K"), ValueFrom: aws.String("arn")},
	})
	if err != nil {
		t.Fatalf("buildTaskDefInput: %v", err)
	}
	if len(base.ContainerDefinitions[0].Secrets) != 0 {
		t.Errorf("base task definition must not be mutated, got secrets %+v", base.ContainerDefinitions[0].Secrets)
	}
}
