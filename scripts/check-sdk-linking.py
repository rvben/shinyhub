"""Check the compiled server keeps only the AWS client operations it needs.

Run on a binary built without -s (go tool nm needs the symbol table). This
catches SDK clients being passed directly through interfaces: CLI template
reflection then retains every exported operation, adding tens of MB.
"""

import argparse
import re
import subprocess


OPERATIONS = {
    "ec2": {"DescribeNetworkInterfaces"},
    "ecs": {
        "RunTask", "StopTask", "DescribeTasks", "ListTasks",
        "DescribeTaskDefinition", "RegisterTaskDefinition",
        "ListTaskDefinitions", "DeregisterTaskDefinition",
    },
    "cloudwatchlogs": {"GetLogEvents"},
    "s3files": {"GetFileSystem"},
    "secretsmanager": {
        "CreateSecret", "PutSecretValue", "DeleteSecret", "ListSecrets",
    },
}
# S3 is intentionally excluded: its SDK stores its own client in an interface
# for S3 Express credentials, so an application-side adapter cannot trim it.
CLIENT_METHOD = re.compile(
    r"github\.com/aws/aws-sdk-go-v2/service/(\w+)\.\(\*Client\)\."
    r"([A-Z]\w+)(?:-fm)?$"
)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    args = parser.parse_args()
    symbols = subprocess.run(
        ["go", "tool", "nm", args.binary], check=True, capture_output=True, text=True
    ).stdout
    linked = {service: set() for service in OPERATIONS}
    for line in symbols.splitlines():
        match = CLIENT_METHOD.search(line)
        if match and match[1] in linked:
            linked[match[1]].add(match[2])

    failures = []
    for service, expected in OPERATIONS.items():
        # Options is SDK configuration access, rather than a remote operation.
        unexpected = linked[service] - expected - {"Options"}
        missing = expected - linked[service]
        if unexpected:
            failures.append(
                f"{service}: {len(unexpected)} unused operations linked: "
                + ", ".join(sorted(unexpected)[:8])
            )
        if missing:
            failures.append(f"{service}: required operations missing: {', '.join(sorted(missing))}")
    if failures:
        raise SystemExit("\n".join(failures))
    print("AWS SDK linking: only required client operations retained")


if __name__ == "__main__":
    main()
