package fargate

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// SDK clients must not be stored directly in interfaces. Reflection in CLI
// templates otherwise keeps every exported SDK operation in the binary. Bound
// method values preserve the SDK's behavior while allowing unused operations to
// be removed by the linker; storing the client in a wrapper field would not.
type ec2ClientFunc func(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)

func (call ec2ClientFunc) DescribeNetworkInterfaces(ctx context.Context, in *ec2.DescribeNetworkInterfacesInput, opts ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return call(ctx, in, opts...)
}

// NewEC2Client exposes only the operation used to resolve task public IPs.
func NewEC2Client(client *ec2.Client) EC2Client {
	return ec2ClientFunc(client.DescribeNetworkInterfaces)
}

type ecsClient struct {
	runTask                  func(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	stopTask                 func(context.Context, *ecs.StopTaskInput, ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	describeTasks            func(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	listTasks                func(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	describeTaskDefinition   func(context.Context, *ecs.DescribeTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	registerTaskDefinition   func(context.Context, *ecs.RegisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	listTaskDefinitions      func(context.Context, *ecs.ListTaskDefinitionsInput, ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error)
	deregisterTaskDefinition func(context.Context, *ecs.DeregisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error)
}

// NewECSClient binds the SDK operations used by the runtime and its cleanup.
func NewECSClient(client *ecs.Client) ECSClient {
	return ecsClient{
		runTask:                  client.RunTask,
		stopTask:                 client.StopTask,
		describeTasks:            client.DescribeTasks,
		listTasks:                client.ListTasks,
		describeTaskDefinition:   client.DescribeTaskDefinition,
		registerTaskDefinition:   client.RegisterTaskDefinition,
		listTaskDefinitions:      client.ListTaskDefinitions,
		deregisterTaskDefinition: client.DeregisterTaskDefinition,
	}
}

func (c ecsClient) RunTask(ctx context.Context, in *ecs.RunTaskInput, opts ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	return c.runTask(ctx, in, opts...)
}

func (c ecsClient) StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	return c.stopTask(ctx, in, opts...)
}

func (c ecsClient) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return c.describeTasks(ctx, in, opts...)
}

func (c ecsClient) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	return c.listTasks(ctx, in, opts...)
}

func (c ecsClient) DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	return c.describeTaskDefinition(ctx, in, opts...)
}

func (c ecsClient) RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	return c.registerTaskDefinition(ctx, in, opts...)
}

func (c ecsClient) ListTaskDefinitions(ctx context.Context, in *ecs.ListTaskDefinitionsInput, opts ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error) {
	return c.listTaskDefinitions(ctx, in, opts...)
}

func (c ecsClient) DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	return c.deregisterTaskDefinition(ctx, in, opts...)
}

type s3FilesDescriberFunc func(context.Context, *s3files.GetFileSystemInput, ...func(*s3files.Options)) (*s3files.GetFileSystemOutput, error)

func (call s3FilesDescriberFunc) GetFileSystem(ctx context.Context, in *s3files.GetFileSystemInput, opts ...func(*s3files.Options)) (*s3files.GetFileSystemOutput, error) {
	return call(ctx, in, opts...)
}

// NewS3FilesDescriber exposes the operation used to resolve the linked bucket.
func NewS3FilesDescriber(client *s3files.Client) S3FilesDescriber {
	return s3FilesDescriberFunc(client.GetFileSystem)
}

type objectPutterFunc func(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)

func (call objectPutterFunc) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return call(ctx, in, opts...)
}

// NewObjectPutter exposes the operation used to create app directory markers.
// Trimming other S3 operations also requires fixing the SDK's internal Express
// credential binding, which currently stores *s3.Client in an interface.
func NewObjectPutter(client *s3.Client) ObjectPutter {
	return objectPutterFunc(client.PutObject)
}

type secretsManagerClient struct {
	createSecret   func(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	putSecretValue func(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	deleteSecret   func(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	listSecrets    func(context.Context, *secretsmanager.ListSecretsInput, ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

func (c secretsManagerClient) CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	return c.createSecret(ctx, in, opts...)
}

func (c secretsManagerClient) PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	return c.putSecretValue(ctx, in, opts...)
}

func (c secretsManagerClient) DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	return c.deleteSecret(ctx, in, opts...)
}

func (c secretsManagerClient) ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	return c.listSecrets(ctx, in, opts...)
}
